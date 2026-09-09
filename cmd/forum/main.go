package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	administrationservice "github.com/gotthboard/gotth-bb/internal/administration"
	"github.com/gotthboard/gotth-bb/internal/app"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/authentikcontrol"
	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/buildinfo"
	"github.com/gotthboard/gotth-bb/internal/config"
	"github.com/gotthboard/gotth-bb/internal/control"
	"github.com/gotthboard/gotth-bb/internal/discovery"
	forumservice "github.com/gotthboard/gotth-bb/internal/forum"
	"github.com/gotthboard/gotth-bb/internal/governance"
	"github.com/gotthboard/gotth-bb/internal/httpui"
	"github.com/gotthboard/gotth-bb/internal/migration"
	moderationservice "github.com/gotthboard/gotth-bb/internal/moderation"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/readiness"
	registrationservice "github.com/gotthboard/gotth-bb/internal/registration"
	siteservice "github.com/gotthboard/gotth-bb/internal/site"
	"github.com/gotthboard/gotth-bb/internal/smtpdelivery"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/gotthboard/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const shutdownTimeout = 15 * time.Second

type databasePool interface {
	auth.SessionDatabase
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
	Close()
}

type poolFactory func(context.Context, *pgxpool.Config) (databasePool, error)
type authenticationFactory func(context.Context, config.Config, auth.SessionDatabase, httpui.URLBuilder) (httpui.AuthenticationService, error)
type cursorKeyringFactory func(string) (discovery.CursorKeyring, error)
type abuseFactory func(config.AbuseConfig) (abuse.Policy, *abuse.RequestLimiter, error)
type registrationControlRuntime struct {
	Objects      authentikcontrol.Objects
	Gateway      registrationservice.ControlGateway
	ReferenceKey [32]byte
	Close        func()
}
type registrationControlFactory func(string, string, string, string) (registrationControlRuntime, error)

type approvalIntakeVerifier interface {
	VerifyApprovalIntake(context.Context, string, string) (registrationservice.Intake, error)
}

func runExpiryReconciler(ctx context.Context, pool databasePool, gateway registrationservice.ControlGateway, logger *slog.Logger) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		result, err := registrationservice.ReconcileExpiredSuspensions(ctx, pool, gateway, time.Now, rand.Reader)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.ErrorContext(context.Background(), "expired identity reconciliation failed")
		} else if result.Claimed != 0 {
			logger.InfoContext(context.Background(), "expired identity reconciliation completed", "claimed", result.Claimed, "completed", result.Completed, "failed", result.Failed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func loadRegistrationControl(objectsPath, issuer, socketPath, fingerprintKeyPath string) (registrationControlRuntime, error) {
	objects, err := authentikcontrol.LoadObjects(objectsPath, issuer)
	if err != nil {
		return registrationControlRuntime{}, fmt.Errorf("load control objects")
	}
	key, err := registrationservice.LoadFingerprintKey(fingerprintKeyPath)
	if err != nil {
		return registrationControlRuntime{}, fmt.Errorf("load fingerprint key")
	}
	client, err := authentikgateway.NewClient(socketPath)
	if err != nil {
		return registrationControlRuntime{}, fmt.Errorf("construct control gateway client")
	}
	return registrationControlRuntime{Objects: objects, Gateway: client, ReferenceKey: key, Close: client.Close}, nil
}

// newLoggedInitialAdministratorClaimer preserves the exact claim result while
// recording an operator-visible failure cause. It deliberately logs no user,
// session, issuer, subject, or request input.
//
// Complexity: construction is tight Theta(1) time and auxiliary space. For
// delegated claim cost D and failure-log cost L, invocation time is O(D+L),
// Omega(D), with no tighter Theta bound because database and log I/O vary;
// local auxiliary space is tight Theta(1) beyond delegated allocations.
func newLoggedInitialAdministratorClaimer(logger *slog.Logger, claim httpui.InitialAdministratorClaimer) (httpui.InitialAdministratorClaimer, error) {
	if logger == nil {
		return nil, fmt.Errorf("administrator claim logger is required")
	}
	if claim == nil {
		return nil, fmt.Errorf("administrator claim service is required")
	}
	return func(ctx context.Context, authentication auth.SessionAuthentication, requestID pgtype.UUID) (governance.InitialAdministratorClaimResult, error) {
		result, err := claim(ctx, authentication, requestID)
		if err != nil {
			logger.ErrorContext(ctx, "initial administrator claim failed", "error", err)
		}
		return result, err
	}, nil
}

// main binds process signals to the tested service runner and reports only a
// bounded top-level error before returning a nonzero process status.
//
// Complexity: local work is tight Theta(1) time and auxiliary space; process
// lifetime and delegated service costs are owned by run.
func main() {
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	finished := make(chan struct{})
	go func() {
		select {
		case <-signals:
			signal.Stop(signals)
			cancel()
		case <-finished:
		}
	}()
	defer func() {
		close(finished)
		signal.Stop(signals)
		cancel()
	}()
	if err := run(ctx, os.LookupEnv, os.Stderr, func(poolContext context.Context, poolConfig *pgxpool.Config) (databasePool, error) {
		return store.OpenPool(poolContext, poolConfig)
	}, func(authContext context.Context, configured config.Config, database auth.SessionDatabase, builder httpui.URLBuilder) (httpui.AuthenticationService, error) {
		return configured.NewAuthenticationService(authContext, nil, database, rand.Reader, time.Now, builder.ValidateReturnPath)
	}, discovery.LoadCursorKeyring, func(configured config.AbuseConfig) (abuse.Policy, *abuse.RequestLimiter, error) {
		policy, err := abuse.LoadPolicy(configured.RulesFile, abuse.RateProfile{
			RequestLimit: configured.RequestLimit, RequestWindow: configured.RequestWindow,
			RequestClientCapacity: configured.RequestClientCapacity,
			PublicationLimit:      configured.PublishLimit, NewAccountLimit: configured.NewAccountPublishLimit,
			PublicationWindow: configured.PublishWindow, NewAccountPeriod: configured.NewAccountPeriod,
		})
		if err != nil {
			return abuse.Policy{}, nil, err
		}
		limiter, err := policy.NewRequestLimiter(rand.Reader, time.Now)
		return policy, limiter, err
	}, loadRegistrationControl, net.Listen); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "gotth-bb: %v\n", err)
		os.Exit(1)
	}
}

// run loads immutable configuration, constructs the HTTP boundary, binds the
// validated numeric listener, and serves until cancellation or failure.
//
// Complexity: for n configuration bytes, configured pool capacity c, initial
// database latency r, served request work q, and shutdown work d, delegated
// time is O(n+N(c)+r+q+d), Omega(1), with no tighter Theta bound established
// because database, network, request, and shutdown costs vary independently.
// Auxiliary space is O(n+A(c)+H(q)), Omega(1), with no tighter Theta bound
// established; N and A are pgx construction costs and H is net/http's concurrent
// request state. Local validation, ownership transfers, and wiring are time
// and auxiliary-space O(1), Omega(1), and tight Theta(1).
func run(
	ctx context.Context,
	lookup config.LookupEnv,
	logOutput io.Writer,
	openPool poolFactory,
	newAuthentication authenticationFactory,
	loadCursorKeyring cursorKeyringFactory,
	loadAbuse abuseFactory,
	loadRegistrationControl registrationControlFactory,
	listen func(string, string) (net.Listener, error),
) error {
	if ctx == nil {
		return fmt.Errorf("service context is required")
	}
	if logOutput == nil {
		return fmt.Errorf("service log output is required")
	}
	if openPool == nil {
		return fmt.Errorf("PostgreSQL pool factory is required")
	}
	if newAuthentication == nil {
		return fmt.Errorf("authentication factory is required")
	}
	if loadCursorKeyring == nil {
		return fmt.Errorf("activity cursor keyring factory is required")
	}
	if loadAbuse == nil {
		return fmt.Errorf("abuse policy factory is required")
	}
	if loadRegistrationControl == nil {
		return fmt.Errorf("registration control loader is required")
	}
	if listen == nil {
		return fmt.Errorf("service listener factory is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service startup canceled: %w", err)
	}
	configured, err := config.Load(lookup)
	if err != nil {
		return fmt.Errorf("load configuration: %w", err)
	}
	cursorKeyring, err := loadCursorKeyring(configured.ActivityCursorKeyringFile)
	if err != nil {
		return fmt.Errorf("load activity cursor keyring failed")
	}
	abusePolicy, requestLimiter, err := loadAbuse(configured.Abuse)
	if err != nil || requestLimiter == nil {
		return fmt.Errorf("load abuse policy failed")
	}
	controlCeilings := control.Ceilings{
		PublishLimit:      configured.Abuse.PublishLimit,
		NewAccountLimit:   configured.Abuse.NewAccountPublishLimit,
		PublishWindow:     configured.Abuse.PublishWindow,
		NewAccountPeriod:  configured.Abuse.NewAccountPeriod,
		SessionIdle:       configured.SessionIdleTimeout,
		AuthRevalidate:    configured.AuthRevalidateInterval,
		SessionMaximumAge: configured.SessionMaxAge,
	}
	if !controlCeilings.Valid() {
		return fmt.Errorf("construct runtime control ceilings failed")
	}
	destinationPolicy := abusePolicy.DestinationPolicy()
	if !destinationPolicy.Valid() {
		return fmt.Errorf("load destination policy failed")
	}
	logger := slog.New(slog.NewJSONHandler(logOutput, &slog.HandlerOptions{Level: configured.LogLevel}))
	abuseObserver, err := abuse.NewObserver(logger)
	if err != nil {
		return fmt.Errorf("construct abuse observer: %w", err)
	}
	release, err := buildinfo.Current()
	if err != nil {
		return fmt.Errorf("load release identity: %w", err)
	}
	urlBuilder, err := httpui.NewURLBuilder(configured.PublicBaseURL, configured.BasePath)
	if err != nil {
		return fmt.Errorf("construct browser URL authority: %w", err)
	}
	poolConfig, err := configured.DatabasePoolConfig()
	if err != nil {
		return fmt.Errorf("configure PostgreSQL pool: %w", err)
	}
	pool, err := openPool(ctx, poolConfig)
	if err != nil {
		if pool != nil {
			pool.Close()
		}
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("open PostgreSQL pool: %w", contextErr)
		}
		return fmt.Errorf("open PostgreSQL pool failed")
	}
	if pool == nil {
		return fmt.Errorf("open PostgreSQL pool returned no pool")
	}
	defer pool.Close()
	authenticationService, err := newAuthentication(ctx, configured, pool, urlBuilder)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return fmt.Errorf("construct authentication service: %w", contextErr)
		}
		return fmt.Errorf("construct authentication service failed")
	}
	if authenticationService == nil {
		return fmt.Errorf("construct authentication service returned no service")
	}
	approvalVerifier, ok := authenticationService.(approvalIntakeVerifier)
	if !ok {
		return fmt.Errorf("construct approval-intake verifier failed")
	}
	registrationControl, err := loadRegistrationControl(configured.AuthentikControlObjectsFile, configured.OIDCIssuerURL.String(), configured.AuthentikControlSocket, configured.InvitationFingerprintKeyFile)
	if err != nil {
		return fmt.Errorf("load registration control runtime failed")
	}
	if registrationControl.Gateway == nil || registrationControl.ReferenceKey == ([32]byte{}) || registrationControl.Close == nil {
		return fmt.Errorf("load registration control runtime returned an invalid runtime")
	}
	defer registrationControl.Close()
	authentikObjects := registrationControl.Objects
	var invitationMailer registrationservice.InvitationMailer
	if configured.SMTP.Configured() {
		mailer, mailerErr := smtpdelivery.New(smtpdelivery.Settings{
			Host: configured.SMTP.Host, Port: configured.SMTP.Port, Username: configured.SMTP.Username,
			From: configured.SMTP.From, PasswordFile: configured.SMTP.PasswordFile,
			TLSMode: configured.SMTP.TLSMode, Timeout: configured.SMTP.Timeout,
		}, configured.OIDCIssuerURL, authentikObjects.Flows.Invitation.Slug)
		if mailerErr != nil {
			return fmt.Errorf("construct invitation mailer failed")
		}
		defer mailer.Close()
		invitationMailer = mailer
	}
	releaseMigrations, err := migration.NewReleaseVerifier(migrations.Files())
	if err != nil {
		return fmt.Errorf("construct migration release verifier: %w", err)
	}
	readinessChecker, err := readiness.New(pool, func(readinessContext context.Context) error {
		return releaseMigrations.Verify(readinessContext, pool)
	}, time.Now, controlCeilings, false)
	if err != nil {
		return fmt.Errorf("construct readiness checker: %w", err)
	}
	queries := db.New(pool)
	claimInitialAdministrator, err := newLoggedInitialAdministratorClaimer(logger, func(setupContext context.Context, authentication auth.SessionAuthentication, requestID pgtype.UUID) (governance.InitialAdministratorClaimResult, error) {
		return governance.ClaimInitialAdministrator(setupContext, pool, time.Now, authentication.Access.UserID, authentication.SessionID, configured.OIDCIssuerURL.String(), configured.BootstrapAdminSubject, requestID)
	})
	if err != nil {
		return fmt.Errorf("construct administrator claim service: %w", err)
	}
	applicationHandler, err := httpui.NewAuthenticatedSiteForumHandler(
		urlBuilder,
		authenticationService,
		func(areaContext context.Context, access auth.AccessContext) ([]store.VisibleAreaSummary, error) {
			return store.ListVisibleAreaSummaries(areaContext, queries, access)
		},
		func(topicContext context.Context, access auth.AccessContext, slug string, page int32) (store.VisibleAreaTopicPage, error) {
			return store.GetVisibleAreaTopicPage(topicContext, queries, slug, page, access)
		},
		store.MaximumTopicPage,
		func(postContext context.Context, access auth.AccessContext, topicID int64, page int32) (store.VisibleTopicPostPage, error) {
			return forumservice.LoadVisibleTopicPostPage(postContext, pool, access, topicID, page)
		},
		store.MaximumPostPage,
		destinationPolicy,
		abuseObserver,
		func(publishContext context.Context, access auth.AccessContext, areaSlug, title, markdown string) (forumservice.PublishResult, error) {
			return forumservice.CreateTopicWithControl(publishContext, pool, controlCeilings, destinationPolicy, access, areaSlug, title, markdown)
		},
		func(publishContext context.Context, access auth.AccessContext, topicID, parentPostID int64, markdown string) (forumservice.PublishResult, error) {
			return forumservice.CreateReplyWithControl(publishContext, pool, controlCeilings, destinationPolicy, access, topicID, parentPostID, markdown)
		},
		func(editContext context.Context, access auth.AccessContext, postID int64) (store.EditablePost, error) {
			return store.GetEditablePost(editContext, queries, postID, access)
		},
		func(editContext context.Context, access auth.AccessContext, postID int64, revision int32, markdown string) (forumservice.EditResult, error) {
			return forumservice.EditPost(editContext, pool, time.Now, destinationPolicy, access, postID, revision, markdown)
		},
		func(deleteContext context.Context, access auth.AccessContext, postID int64, revision int32) (forumservice.DeleteResult, error) {
			return forumservice.DeletePost(deleteContext, pool, time.Now, access, postID, revision)
		},
		func(moderationContext context.Context, access auth.AccessContext, topicID int64, lock bool, reason string, requestID pgtype.UUID) (moderationservice.TopicTransitionResult, error) {
			return moderationservice.ChangeTopicLock(moderationContext, pool, time.Now, access, topicID, lock, reason, requestID)
		},
		func(moderationContext context.Context, access auth.AccessContext, topicID int64, hide bool, reason string, requestID pgtype.UUID) (moderationservice.TopicTransitionResult, error) {
			return moderationservice.ChangeTopicVisibility(moderationContext, pool, time.Now, access, topicID, hide, reason, requestID)
		},
		func(moderationContext context.Context, access auth.AccessContext, userID int64) (store.ModerationUserStatus, error) {
			return store.GetModerationUserStatus(moderationContext, queries, access, userID, time.Now())
		},
		func(moderationContext context.Context, access auth.AccessContext, userID int64, suspend bool, reason string, requestID pgtype.UUID) (moderationservice.UserSuspensionResult, error) {
			return registrationservice.ChangeUserSuspension(moderationContext, pool, registrationControl.Gateway, time.Now, access, userID, suspend, reason, requestID)
		},
		func(administrationContext context.Context, access auth.AccessContext) (administrationservice.AreaManagementPage, error) {
			return administrationservice.LoadAreaManagement(administrationContext, queries, access)
		},
		func(administrationContext context.Context, access auth.AccessContext, input administrationservice.AreaInput, requestID pgtype.UUID) (administrationservice.AreaMutationResult, error) {
			return administrationservice.CreateArea(administrationContext, pool, time.Now, access, input, requestID)
		},
		func(administrationContext context.Context, access auth.AccessContext, areaID int64, input administrationservice.AreaInput, requestID pgtype.UUID) (administrationservice.AreaMutationResult, error) {
			return administrationservice.UpdateArea(administrationContext, pool, time.Now, access, areaID, input, requestID)
		},
		httpui.ReportHTTPServices{
			Create: func(reportContext context.Context, access auth.AccessContext, target moderationservice.ReportTargetType, targetID int64, reason string) (moderationservice.ReportResult, error) {
				return moderationservice.CreateReport(reportContext, pool, time.Now, access, target, targetID, reason)
			},
			List: func(reportContext context.Context, access auth.AccessContext, page int32) (store.ModerationReportPage, error) {
				return store.ListModerationReports(reportContext, queries, access, page, time.Now())
			},
			Load: func(reportContext context.Context, access auth.AccessContext, reportID int64) (store.ModerationReportDetail, error) {
				return store.GetModerationReport(reportContext, queries, access, reportID, time.Now())
			},
			Process: func(reportContext context.Context, access auth.AccessContext, reportID int64, action moderationservice.ReportAction, text string, requestID pgtype.UUID) (moderationservice.ReportActionResult, error) {
				return moderationservice.ProcessReport(reportContext, pool, time.Now, access, reportID, action, text, requestID)
			},
			Extended: func(moderationContext context.Context, access auth.AccessContext, input moderationservice.ExtendedActionInput, requestID pgtype.UUID) (moderationservice.ExtendedActionResult, error) {
				return moderationservice.ApplyExtendedAction(moderationContext, pool, time.Now, access, input, requestID)
			},
		},
		httpui.DiscoveryHTTPServices{
			Search: func(searchContext context.Context, request discovery.SearchRequest, access auth.AccessContext) (discovery.SearchPage, error) {
				return discovery.Search(searchContext, pool, request, access)
			},
			Activity: func(activityContext context.Context, cursor *discovery.AuthenticatedCursor, access auth.AccessContext) (discovery.ActivityPage, error) {
				return discovery.RecentActivity(activityContext, pool, cursor, cursorKeyring, access)
			},
			DirectPost: func(postContext context.Context, postID int64, access auth.AccessContext) (discovery.DirectPost, error) {
				return discovery.GetDirectPost(postContext, queries, postID, access)
			},
		},
		cursorKeyring.VerifyCursor,
		httpui.UnreadHTTPServices{
			FirstUnread: func(unreadContext context.Context, access auth.AccessContext, topicID int64) (forumservice.FirstUnreadTarget, error) {
				return forumservice.FirstUnread(unreadContext, pool, access, topicID)
			},
			MarkRead: func(readContext context.Context, access auth.AccessContext, topicID int64) error {
				return forumservice.MarkTopicRead(readContext, pool, access, topicID)
			},
		},
		httpui.SiteHTTPServices{
			Shell: func(siteContext context.Context) (siteservice.ShellPresentation, error) {
				return siteservice.LoadShell(siteContext, queries)
			},
			Rules: func(siteContext context.Context) (siteservice.PublicRules, error) {
				return siteservice.LoadRules(siteContext, queries)
			},
			Editable: func(siteContext context.Context, access auth.AccessContext) (siteservice.EditableSettings, error) {
				return siteservice.LoadEditable(siteContext, queries, access, time.Now())
			},
			Update: func(siteContext context.Context, access auth.AccessContext, input siteservice.SettingsInput, requestID pgtype.UUID) (siteservice.MutationResult, error) {
				return siteservice.UpdateSettings(siteContext, pool, time.Now, destinationPolicy, access, input, requestID)
			},
			Control: func(siteContext context.Context) (control.Settings, error) {
				return control.Load(siteContext, queries, controlCeilings)
			},
			Registration: &httpui.RegistrationHTTPServices{
				LoadSettings: func(registrationContext context.Context) (control.Settings, error) {
					return control.Load(registrationContext, queries, controlCeilings)
				},
				Issuer: configured.OIDCIssuerURL, OpenFlowSlug: authentikObjects.Flows.Open.Slug,
				ApprovalFlowSlug: authentikObjects.Flows.Approval.Slug, SMTPConfigured: configured.SMTP.Configured(),
			},
			Administration: &httpui.AdministrationHTTPServices{
				Dashboard: func(adminContext context.Context, access auth.AccessContext) (administrationservice.Dashboard, error) {
					return administrationservice.LoadDashboard(adminContext, pool, access)
				},
				ListAccounts: func(adminContext context.Context, access auth.AccessContext, after int64) (administrationservice.AccountPage, error) {
					return administrationservice.ListAccounts(adminContext, queries, access, time.Now(), after)
				},
				LoadAccount: func(adminContext context.Context, access auth.AccessContext, userID int64) (administrationservice.AccountSummary, error) {
					return administrationservice.LoadAccount(adminContext, queries, access, time.Now(), userID)
				},
				ListAccountGroups: func(adminContext context.Context, access auth.AccessContext, userID, after int64) (administrationservice.AccountGroupPage, error) {
					return administrationservice.ListAccountGroups(adminContext, queries, access, time.Now(), userID, after)
				},
				ListGroups: func(adminContext context.Context, access auth.AccessContext, after int64) (administrationservice.GroupPage, error) {
					return administrationservice.ListGroups(adminContext, queries, access, time.Now(), after)
				},
				CreateGroup: func(adminContext context.Context, access auth.AccessContext, name, reason string, requestID pgtype.UUID) (administrationservice.GroupMutationResult, error) {
					return administrationservice.CreateGroup(adminContext, pool, time.Now, access, name, reason, requestID)
				},
				RenameGroup: func(adminContext context.Context, access auth.AccessContext, groupID int64, name, reason string, revision int64, requestID pgtype.UUID) (administrationservice.GroupMutationResult, error) {
					return administrationservice.RenameGroup(adminContext, pool, time.Now, access, groupID, name, reason, revision, requestID)
				},
				ChangeMembership: func(adminContext context.Context, access auth.AccessContext, userID, groupID int64, grant bool, reason string, revision int64, requestID pgtype.UUID) (administrationservice.AccountMutationResult, error) {
					return administrationservice.ChangeGroupMembership(adminContext, pool, time.Now, access, userID, groupID, grant, reason, revision, requestID)
				},
				ChangeRole: func(adminContext context.Context, access auth.AccessContext, userID int64, role, expected policy.Role, reason string, revision int64, requestID pgtype.UUID) (administrationservice.AccountMutationResult, error) {
					return administrationservice.ChangeAccountRole(adminContext, pool, time.Now, access, userID, role, expected, reason, revision, requestID)
				},
				ListAreas: func(adminContext context.Context, access auth.AccessContext, afterOrder int32, afterID int64) (administrationservice.AreaPage, error) {
					return administrationservice.ListAreaPage(adminContext, queries, access, time.Now(), afterOrder, afterID)
				},
				LoadArea: func(adminContext context.Context, access auth.AccessContext, areaID, after int64) (administrationservice.AreaDetail, error) {
					return administrationservice.LoadAreaDetail(adminContext, queries, access, time.Now(), areaID, after)
				},
				CreateArea: func(adminContext context.Context, access auth.AccessContext, input administrationservice.AreaCoreInput, requestID pgtype.UUID) (administrationservice.AreaCompletionResult, error) {
					return administrationservice.CreateAreaCompletion(adminContext, pool, time.Now, access, input, requestID)
				},
				UpdateArea: func(adminContext context.Context, access auth.AccessContext, areaID int64, input administrationservice.AreaCoreInput, requestID pgtype.UUID) (administrationservice.AreaCompletionResult, error) {
					return administrationservice.UpdateAreaCompletion(adminContext, pool, time.Now, access, areaID, input, requestID)
				},
				ChangeAreaGroup: func(adminContext context.Context, access auth.AccessContext, areaID, groupID int64, grant bool, reason string, revision int64, requestID pgtype.UUID) (administrationservice.AreaCompletionResult, error) {
					return administrationservice.ChangeAreaGroup(adminContext, pool, time.Now, access, areaID, groupID, grant, reason, revision, requestID)
				},
				Registrations: &httpui.RegistrationAdministrationHTTPServices{
					List: func(adminContext context.Context, access auth.AccessContext, after int64) (registrationservice.PendingPage, error) {
						return registrationservice.ListPendingAdministration(adminContext, queries, registrationControl.Gateway, access, time.Now(), after, registrationControl.ReferenceKey)
					},
					Decide: func(adminContext context.Context, access auth.AccessContext, input registrationservice.DecisionInput) (registrationservice.DecisionResult, error) {
						return registrationservice.Decide(adminContext, pool, registrationControl.Gateway, time.Now, access, input, registrationControl.ReferenceKey)
					},
					Adopt: func(adminContext context.Context, access auth.AccessContext, input registrationservice.AdoptionInput) (registrationservice.AdoptionResult, error) {
						return registrationservice.Adopt(adminContext, pool, registrationControl.Gateway, time.Now, access, input, registrationControl.ReferenceKey)
					},
				},
				Invitations: &httpui.InvitationAdministrationHTTPServices{
					List: func(adminContext context.Context, access auth.AccessContext) (registrationservice.InvitationPage, error) {
						return registrationservice.ListInvitations(adminContext, queries, registrationControl.Gateway, access, time.Now(), registrationControl.ReferenceKey)
					},
					Create: func(adminContext context.Context, access auth.AccessContext, input registrationservice.InvitationInput) (registrationservice.InvitationResult, error) {
						return registrationservice.CreateInvitation(adminContext, pool, registrationControl.Gateway, invitationMailer, time.Now, access, input, registrationControl.ReferenceKey, authentikObjects.Flows.Invitation.UUID)
					},
					Revoke: func(adminContext context.Context, access auth.AccessContext, input registrationservice.InvitationRevocationInput) (registrationservice.InvitationRevocationResult, error) {
						return registrationservice.RevokeInvitation(adminContext, pool, registrationControl.Gateway, time.Now, access, input, registrationControl.ReferenceKey)
					},
					Clock: time.Now, Issuer: configured.OIDCIssuerURL, FlowSlug: authentikObjects.Flows.Invitation.Slug, SMTPConfigured: configured.SMTP.Configured(),
				},
			},
		},
		configured.RegistrationURL,
		configured.RegistrationEnabled,
		func(setupContext context.Context, authentication auth.SessionAuthentication) (governance.InitialAdministratorSetupStatus, error) {
			return governance.LoadInitialAdministratorSetup(setupContext, queries, time.Now, authentication.Access.UserID, configured.OIDCIssuerURL.String(), configured.BootstrapAdminSubject)
		},
		claimInitialAdministrator,
		configured.SessionCookieName,
		configured.PublicBaseURL.Scheme == "https",
		readinessChecker.Check,
	)
	if err != nil {
		return fmt.Errorf("construct authenticated HTTP routes: %w", err)
	}
	applicationHandler, err = httpui.NewRegistrationControlHandler(applicationHandler, httpui.RegistrationControlHTTPServices{
		LoadSettings: func(registrationContext context.Context) (control.Settings, error) {
			return control.Load(registrationContext, queries, controlCeilings)
		},
		VerifyApproval: func(registrationContext context.Context, raw string) (registrationservice.Intake, error) {
			return approvalVerifier.VerifyApprovalIntake(registrationContext, raw, authentikObjects.Flows.Approval.UUID)
		},
		AcceptApproval: func(registrationContext context.Context, intake registrationservice.Intake) error {
			return registrationservice.AcceptIntake(registrationContext, queries, time.Now, intake)
		},
		SMTPConfigured: configured.SMTP.Configured(),
	})
	if err != nil {
		return fmt.Errorf("construct registration control routes: %w", err)
	}
	applicationHandler, err = httpui.NewFooterLoadTimesHandler(applicationHandler, release.Version, time.Now)
	if err != nil {
		return fmt.Errorf("construct footer load-time boundary: %w", err)
	}
	applicationHandler, err = httpui.NewRequestAdmissionHandler(applicationHandler, requestLimiter, abuseObserver, configured.Environment == config.EnvironmentProduction)
	if err != nil {
		return fmt.Errorf("construct request admission boundary: %w", err)
	}
	handler, err := app.NewHTTPHandler(applicationHandler, logger, rand.Reader, time.Now)
	if err != nil {
		return fmt.Errorf("construct HTTP handler: %w", err)
	}
	handler, err = httpui.NewBrowserSecurityHandler(handler)
	if err != nil {
		return fmt.Errorf("construct browser security boundary: %w", err)
	}
	server, err := app.NewHTTPServer(configured, handler, slog.NewLogLogger(logger.Handler(), slog.LevelError))
	if err != nil {
		return fmt.Errorf("construct HTTP server: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("service startup canceled: %w", err)
	}
	listener, err := listen("tcp", configured.ListenAddr.String())
	if err != nil {
		return fmt.Errorf("listen for HTTP: %w", err)
	}
	if err := ctx.Err(); err != nil {
		if closeErr := listener.Close(); closeErr != nil {
			return errors.Join(fmt.Errorf("service startup canceled: %w", err), fmt.Errorf("close canceled listener: %w", closeErr))
		}
		return fmt.Errorf("service startup canceled: %w", err)
	}
	logger.InfoContext(context.Background(), "service starting", "version", release.Version, "commit", release.Commit)
	reconcilerContext, stopReconciler := context.WithCancel(ctx)
	var reconcilerDone chan struct{}
	if configured.Environment != config.EnvironmentTest {
		reconcilerDone = make(chan struct{})
		go func() {
			defer close(reconcilerDone)
			runExpiryReconciler(reconcilerContext, pool, registrationControl.Gateway, logger)
		}()
	}
	serverErr := app.RunHTTPServer(ctx, server, listener, shutdownTimeout)
	stopReconciler()
	if reconcilerDone != nil {
		<-reconcilerDone
	}
	if serverErr != nil {
		return serverErr
	}
	logger.InfoContext(context.Background(), "service stopped")
	return nil
}
