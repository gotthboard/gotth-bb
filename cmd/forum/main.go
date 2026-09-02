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

	"git.dannyhunn.com/agents/gotth-bb/internal/app"
	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/buildinfo"
	"git.dannyhunn.com/agents/gotth-bb/internal/config"
	forumservice "git.dannyhunn.com/agents/gotth-bb/internal/forum"
	"git.dannyhunn.com/agents/gotth-bb/internal/governance"
	"git.dannyhunn.com/agents/gotth-bb/internal/httpui"
	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	moderationservice "git.dannyhunn.com/agents/gotth-bb/internal/moderation"
	"git.dannyhunn.com/agents/gotth-bb/internal/readiness"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const shutdownTimeout = 15 * time.Second

type databasePool interface {
	auth.SessionDatabase
	Close()
}

type poolFactory func(context.Context, *pgxpool.Config) (databasePool, error)
type authenticationFactory func(context.Context, config.Config, auth.SessionDatabase, httpui.URLBuilder) (httpui.AuthenticationService, error)

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
	}, net.Listen); err != nil {
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
	releaseMigrations, err := migration.NewReleaseVerifier(migrations.Files())
	if err != nil {
		return fmt.Errorf("construct migration release verifier: %w", err)
	}
	readinessChecker, err := readiness.New(pool, func(readinessContext context.Context) error {
		return releaseMigrations.Verify(readinessContext, pool)
	}, time.Now)
	if err != nil {
		return fmt.Errorf("construct readiness checker: %w", err)
	}
	queries := db.New(pool)
	applicationHandler, err := httpui.NewAuthenticatedModeratedForumHandler(
		urlBuilder,
		authenticationService,
		func(areaContext context.Context, access auth.AccessContext) ([]db.Area, error) {
			return store.ListVisibleAreas(areaContext, queries, access)
		},
		func(topicContext context.Context, access auth.AccessContext, slug string, page int32) (store.VisibleAreaTopicPage, error) {
			return store.GetVisibleAreaTopicPage(topicContext, queries, slug, page, access)
		},
		store.MaximumTopicPage,
		func(postContext context.Context, access auth.AccessContext, topicID int64, page int32) (store.VisibleTopicPostPage, error) {
			return store.GetVisibleTopicPostPage(postContext, queries, topicID, page, access)
		},
		store.MaximumPostPage,
		func(publishContext context.Context, access auth.AccessContext, areaSlug, title, markdown string) (forumservice.PublishResult, error) {
			return forumservice.CreateTopic(publishContext, pool, time.Now, access, areaSlug, title, markdown)
		},
		func(publishContext context.Context, access auth.AccessContext, topicID int64, markdown string) (forumservice.PublishResult, error) {
			return forumservice.CreateReply(publishContext, pool, time.Now, access, topicID, markdown)
		},
		func(editContext context.Context, access auth.AccessContext, postID int64) (store.EditablePost, error) {
			return store.GetEditablePost(editContext, queries, postID, access)
		},
		func(editContext context.Context, access auth.AccessContext, postID int64, revision int32, markdown string) (forumservice.EditResult, error) {
			return forumservice.EditPost(editContext, pool, time.Now, access, postID, revision, markdown)
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
			return moderationservice.ChangeUserSuspension(moderationContext, pool, time.Now, access, userID, suspend, reason, requestID)
		},
		configured.RegistrationURL,
		configured.RegistrationEnabled,
		func(setupContext context.Context, authentication auth.SessionAuthentication) (governance.InitialAdministratorSetupStatus, error) {
			return governance.LoadInitialAdministratorSetup(setupContext, queries, time.Now, authentication.Access.UserID, configured.OIDCIssuerURL.String(), configured.BootstrapAdminSubject)
		},
		func(setupContext context.Context, authentication auth.SessionAuthentication, requestID pgtype.UUID) (governance.InitialAdministratorClaimResult, error) {
			return governance.ClaimInitialAdministrator(setupContext, pool, time.Now, authentication.Access.UserID, authentication.SessionID, configured.OIDCIssuerURL.String(), configured.BootstrapAdminSubject, requestID)
		},
		configured.SessionCookieName,
		configured.PublicBaseURL.Scheme == "https",
		readinessChecker.Check,
	)
	if err != nil {
		return fmt.Errorf("construct authenticated HTTP routes: %w", err)
	}
	applicationHandler, err = httpui.NewFooterLoadTimesHandler(applicationHandler, release.Version, time.Now)
	if err != nil {
		return fmt.Errorf("construct footer load-time boundary: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(logOutput, &slog.HandlerOptions{Level: configured.LogLevel}))
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
	if err := app.RunHTTPServer(ctx, server, listener, shutdownTimeout); err != nil {
		return err
	}
	logger.InfoContext(context.Background(), "service stopped")
	return nil
}
