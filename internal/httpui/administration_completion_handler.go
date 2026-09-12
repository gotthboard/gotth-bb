package httpui

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/gotthboard/gotth-bb/internal/administration"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/registration"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	maximumAdministrationSmallFormBytes = 16_384
	maximumAdministrationAreaFormBytes  = 65_536
)

type AdministrationHTTPServices struct {
	Dashboard         func(context.Context, auth.AccessContext) (administration.Dashboard, error)
	ListAccounts      func(context.Context, auth.AccessContext, int64) (administration.AccountPage, error)
	LoadAccount       func(context.Context, auth.AccessContext, int64) (administration.AccountSummary, error)
	ListAccountGroups func(context.Context, auth.AccessContext, int64, int64) (administration.AccountGroupPage, error)
	ListGroups        func(context.Context, auth.AccessContext, int64) (administration.GroupPage, error)
	CreateGroup       func(context.Context, auth.AccessContext, string, string, pgtype.UUID) (administration.GroupMutationResult, error)
	RenameGroup       func(context.Context, auth.AccessContext, int64, string, string, int64, pgtype.UUID) (administration.GroupMutationResult, error)
	ChangeMembership  func(context.Context, auth.AccessContext, int64, int64, bool, string, int64, pgtype.UUID) (administration.AccountMutationResult, error)
	ChangeRole        func(context.Context, auth.AccessContext, int64, policy.Role, policy.Role, string, int64, pgtype.UUID) (administration.AccountMutationResult, error)
	ReconcileIdentity func(context.Context, auth.AccessContext, int64, string, pgtype.UUID) (registration.IdentityReconciliationResult, error)
	ListAreas         func(context.Context, auth.AccessContext, int32, int64) (administration.AreaPage, error)
	LoadArea          func(context.Context, auth.AccessContext, int64, int64) (administration.AreaDetail, error)
	CreateArea        func(context.Context, auth.AccessContext, administration.AreaCoreInput, pgtype.UUID) (administration.AreaCompletionResult, error)
	UpdateArea        func(context.Context, auth.AccessContext, int64, administration.AreaCoreInput, pgtype.UUID) (administration.AreaCompletionResult, error)
	ChangeAreaGroup   func(context.Context, auth.AccessContext, int64, int64, bool, string, int64, pgtype.UUID) (administration.AreaCompletionResult, error)
	Registrations     *RegistrationAdministrationHTTPServices
	Invitations       *InvitationAdministrationHTTPServices
}

type RegistrationAdministrationHTTPServices struct {
	List   func(context.Context, auth.AccessContext, int64) (registration.PendingPage, error)
	Decide func(context.Context, auth.AccessContext, registration.DecisionInput) (registration.DecisionResult, error)
	Adopt  func(context.Context, auth.AccessContext, registration.AdoptionInput) (registration.AdoptionResult, error)
}

type InvitationAdministrationHTTPServices struct {
	List           func(context.Context, auth.AccessContext) (registration.InvitationPage, error)
	Create         func(context.Context, auth.AccessContext, registration.InvitationInput) (registration.InvitationResult, error)
	Revoke         func(context.Context, auth.AccessContext, registration.InvitationRevocationInput) (registration.InvitationRevocationResult, error)
	Clock          func() time.Time
	Issuer         url.URL
	FlowSlug       string
	SMTPConfigured bool
}

type administrationDashboardView struct {
	ObservedAt                                                              string
	Users, Members, Moderators, Administrators, ActiveUsers, SuspendedUsers int64
	Topics, Posts, OpenReports, InReviewReports                             int64
}

type administrationAccountListItem struct{ DisplayName, Role, Status, URL string }
type administrationAccountsView struct {
	Accounts []administrationAccountListItem
	NextURL  string
}
type administrationMembershipView struct {
	ID                             int64
	Name, Action, Label, ActionURL string
}
type administrationAccountView struct {
	DisplayName, Role, Status, Revision, RoleAction, ReconcileAction, AuthentikSyncState, CSRFToken, NextGroupsURL string
	Groups                                                                                                         []administrationMembershipView
}
type administrationGroupItemView struct{ Name, Revision, ActionURL string }
type administrationGroupsView struct {
	Groups                           []administrationGroupItemView
	CreateAction, CSRFToken, NextURL string
}
type administrationAreaListItem struct {
	Name, Slug, Visibility, PostingMode, URL string
	GroupCount                               int64
}
type administrationAreasView struct {
	Areas                            []administrationAreaListItem
	CreateAction, CSRFToken, NextURL string
}
type administrationAreaView struct {
	Name, Slug, Description, DisplayOrder, Visibility, PostingMode, Revision, ActionURL, CSRFToken, NextGroupsURL string
	Groups                                                                                                        []administrationMembershipView
}

type administrationRegistrationView struct {
	ID, DisplayName, VerifiedEmail, Status, Revision, IntakeAt, Failure, ApproveURL, RejectURL string
}
type administrationRegistrationsView struct {
	Registrations                 []administrationRegistrationView
	Orphans                       []administrationRegistrationOrphanView
	CSRFToken, NextURL            string
	RemoteUnavailable, RemoteMore bool
}
type administrationRegistrationOrphanView struct {
	DisplayName, VerifiedEmail, AdoptURL string
}
type administrationInvitationView struct {
	Name, Status, Delivery, Failure, ExpiresAt, RevokeURL string
}
type administrationInvitationsView struct {
	Invitations                                                       []administrationInvitationView
	ActionURL, CSRFToken, IdempotencyKey, ExpiryReference, OneTimeURL string
	More                                                              bool
	SMTPConfigured                                                    bool
}

var administrationAdoptionHandle = regexp.MustCompile(`^[A-Za-z0-9_-]{87}$`)
var administrationInvitationToken = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func validAdministrationHTTPServices(services AdministrationHTTPServices) bool {
	registrationsValid := services.Registrations == nil || services.Registrations.List != nil && services.Registrations.Decide != nil && services.Registrations.Adopt != nil
	invitationsValid := services.Invitations == nil || services.Invitations.List != nil && services.Invitations.Create != nil && services.Invitations.Revoke != nil && services.Invitations.Clock != nil && services.Invitations.Issuer.Scheme == "https" && services.Invitations.Issuer.Host != "" && registrationFlowSlug.MatchString(services.Invitations.FlowSlug)
	return registrationsValid && invitationsValid && services.Dashboard != nil && services.ListAccounts != nil && services.LoadAccount != nil && services.ListAccountGroups != nil && services.ListGroups != nil &&
		services.CreateGroup != nil && services.RenameGroup != nil && services.ChangeMembership != nil && services.ChangeRole != nil && services.ReconcileIdentity != nil &&
		services.ListAreas != nil && services.LoadArea != nil && services.CreateArea != nil && services.UpdateArea != nil && services.ChangeAreaGroup != nil
}

func newAdministrationCompletionHandler(builder URLBuilder, services AdministrationHTTPServices) (http.Handler, error) {
	if !validAdministrationHTTPServices(services) {
		return nil, fmt.Errorf("browser administration services are incomplete")
	}
	adminURL, err := builder.Path("admin")
	if err != nil {
		return nil, err
	}
	loginURL, err := builder.PathWithQuery([]string{"login"}, url.Values{"return": {adminURL}})
	if err != nil {
		return nil, err
	}
	revalidationURL, err := builder.PathWithQuery([]string{"auth", "revalidate"}, url.Values{"return": {adminURL}})
	if err != nil {
		return nil, err
	}
	views := map[string]pageView{}
	for _, definition := range []struct {
		key, title string
		segments   []string
	}{
		{key: "dashboard", title: "Administration", segments: []string{"admin"}},
		{key: "accounts", title: "Accounts", segments: []string{"admin", "accounts"}},
		{key: "account", title: "Account"},
		{key: "groups", title: "Groups", segments: []string{"admin", "groups"}},
		{key: "areas", title: "Areas", segments: []string{"admin", "areas"}},
		{key: "area", title: "Area"},
		{key: "registrations", title: "Pending registrations", segments: []string{"admin", "registrations"}},
		{key: "invitations", title: "Invitations", segments: []string{"admin", "invitations"}},
	} {
		view, viewErr := newPageView(builder, definition.title, definition.segments...)
		if viewErr != nil {
			return nil, viewErr
		}
		if len(definition.segments) == 0 {
			view.CanonicalURL = ""
		}
		views[definition.key] = view
	}
	if services.Registrations != nil {
		registrationsURL, buildErr := builder.Path("admin", "registrations")
		if buildErr != nil {
			return nil, buildErr
		}
		for key, view := range views {
			view.RegistrationsURL = registrationsURL
			views[key] = view
		}
	}
	if services.Invitations != nil {
		invitationsURL, buildErr := builder.Path("admin", "invitations")
		if buildErr != nil {
			return nil, buildErr
		}
		for key, view := range views {
			view.InvitationsURL = invitationsURL
			views[key] = view
		}
	}
	authorized := func(response http.ResponseWriter, request *http.Request) (auth.AccessContext, bool) {
		response.Header().Set("Cache-Control", "private, no-store")
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
			serveSessionRedirect(response, request, loginURL)
			return auth.AccessContext{}, false
		}
		if authentication.RequiresRevalidation {
			serveSessionRedirect(response, request, revalidationURL)
			return auth.AccessContext{}, false
		}
		if !policy.CanAdminister(authentication.Access) {
			renderAdministrationError(response, request, views["dashboard"], http.StatusForbidden, "Administration denied", "Your current account cannot administer this board.")
			return auth.AccessContext{}, false
		}
		return authentication.Access, true
	}
	render := func(response http.ResponseWriter, request *http.Request, view pageView, body templ.Component) {
		if err := renderResponse(response, request, http.StatusOK, administrationPage(view, body), administrationContent(view, body)); err != nil {
			panic(err)
		}
	}
	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Get("/admin", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		page, loadErr := services.Dashboard(request.Context(), actor)
		if loadErr != nil {
			serveAdministrationServiceError(response, request, views["dashboard"], loadErr)
			return
		}
		render(response, request, views["dashboard"], administrationDashboardBody(administrationDashboardView{ObservedAt: page.ObservedAt.Format(time.RFC3339), Users: page.Users, Members: page.Members, Moderators: page.Moderators, Administrators: page.Administrators, ActiveUsers: page.ActiveUsers, SuspendedUsers: page.SuspendedUsers, Topics: page.Topics, Posts: page.Posts, OpenReports: page.OpenReports, InReviewReports: page.InReviewReports}))
	})
	router.Get("/admin/accounts", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		after, _ := parseAdministrationCursor(request.URL.Query(), "after")
		page, loadErr := services.ListAccounts(request.Context(), actor, after)
		if loadErr != nil {
			serveAdministrationServiceError(response, request, views["accounts"], loadErr)
			return
		}
		presentation := administrationAccountsView{Accounts: make([]administrationAccountListItem, len(page.Accounts))}
		for index, account := range page.Accounts {
			target, _ := builder.Path("admin", "accounts", strconv.FormatInt(account.ID, 10))
			status := "active"
			if account.Suspended {
				status = "suspended"
			}
			presentation.Accounts[index] = administrationAccountListItem{DisplayName: account.DisplayName, Role: administrationRoleName(account.Role), Status: status, URL: target}
		}
		if page.NextAfter > 0 {
			presentation.NextURL, _ = builder.PathWithQuery([]string{"admin", "accounts"}, url.Values{"after": {strconv.FormatInt(page.NextAfter, 10)}})
		}
		render(response, request, views["accounts"], administrationAccountsBody(presentation))
	})
	if services.Registrations != nil {
		router.Get("/admin/registrations", func(response http.ResponseWriter, request *http.Request) {
			actor, ok := authorized(response, request)
			if !ok {
				return
			}
			after, _ := parseAdministrationCursor(request.URL.Query(), "after")
			page, loadErr := services.Registrations.List(request.Context(), actor, after)
			if loadErr != nil {
				serveAdministrationServiceError(response, request, views["registrations"], loadErr)
				return
			}
			presentation := administrationRegistrationsView{
				Registrations: make([]administrationRegistrationView, len(page.Registrations)),
				Orphans:       make([]administrationRegistrationOrphanView, len(page.Orphans)),
				CSRFToken:     csrfTokenFromContext(request.Context()), RemoteUnavailable: page.RemoteUnavailable, RemoteMore: page.RemoteMore,
			}
			for index, pending := range page.Registrations {
				id := strconv.FormatInt(pending.ID, 10)
				approveURL, _ := builder.Path("admin", "registrations", id, "approve")
				rejectURL, _ := builder.Path("admin", "registrations", id, "reject")
				presentation.Registrations[index] = administrationRegistrationView{
					ID: id, DisplayName: pending.DisplayName, VerifiedEmail: pending.VerifiedEmail,
					Status: pending.Status, Revision: strconv.FormatInt(pending.Revision, 10),
					IntakeAt: pending.IntakeAt.Format(time.RFC3339), Failure: pending.ReconciliationClass,
					ApproveURL: approveURL, RejectURL: rejectURL,
				}
			}
			for index, orphan := range page.Orphans {
				adoptURL, _ := builder.Path("admin", "registrations", orphan.Handle, "adopt")
				presentation.Orphans[index] = administrationRegistrationOrphanView{DisplayName: orphan.DisplayName, VerifiedEmail: orphan.VerifiedEmail, AdoptURL: adoptURL}
			}
			if page.NextAfter > 0 {
				presentation.NextURL, _ = builder.PathWithQuery([]string{"admin", "registrations"}, url.Values{"after": {strconv.FormatInt(page.NextAfter, 10)}})
			}
			render(response, request, views["registrations"], administrationRegistrationsBody(presentation))
		})
		decide := func(decision registration.Decision) http.HandlerFunc {
			return func(response http.ResponseWriter, request *http.Request) {
				actor, ok := authorized(response, request)
				if !ok {
					return
				}
				registrationID, _ := parseCanonicalPositiveID(chi.URLParam(request, "registrationID"))
				form, ok := parseAdministrationForm(response, request, views["registrations"], maximumAdministrationSmallFormBytes, []string{"_csrf", "reason", "revision"})
				if !ok {
					return
				}
				revision, parseErr := parsePositiveFormID(form.Get("revision"))
				requestID, requestErr := moderationRequestUUID(request.Context())
				if parseErr != nil || requestErr != nil {
					renderAdministrationError(response, request, views["registrations"], http.StatusBadRequest, "Invalid form", "Reload pending registrations and try again.")
					return
				}
				result, decisionErr := services.Registrations.Decide(request.Context(), actor, registration.DecisionInput{RegistrationID: registrationID, Revision: revision, Decision: decision, Reason: form.Get("reason"), RequestID: requestID})
				if decisionErr != nil {
					serveAdministrationMutationError(response, request, views["registrations"], decisionErr)
					return
				}
				terminal := decision == registration.Approve && result.Status == "approved" || decision == registration.Reject && result.Status == "rejected"
				if !terminal || result.Revision <= revision || result.AuditID < 0 {
					serveAdministrationServiceError(response, request, views["registrations"], errors.New("invalid registration decision result"))
					return
				}
				destination, _ := builder.Path("admin", "registrations")
				serveMutationNavigation(response, request, destination)
			}
		}
		router.Post("/admin/registrations/{registrationID}/approve", decide(registration.Approve))
		router.Post("/admin/registrations/{registrationID}/reject", decide(registration.Reject))
		router.Post("/admin/registrations/{handle}/adopt", func(response http.ResponseWriter, request *http.Request) {
			actor, ok := authorized(response, request)
			if !ok {
				return
			}
			form, ok := parseAdministrationForm(response, request, views["registrations"], maximumAdministrationSmallFormBytes, []string{"_csrf", "reason"})
			if !ok {
				return
			}
			requestID, requestErr := moderationRequestUUID(request.Context())
			if requestErr != nil {
				serveAdministrationServiceError(response, request, views["registrations"], requestErr)
				return
			}
			result, adoptErr := services.Registrations.Adopt(request.Context(), actor, registration.AdoptionInput{Handle: chi.URLParam(request, "handle"), Reason: form.Get("reason"), RequestID: requestID})
			if adoptErr != nil {
				serveAdministrationMutationError(response, request, views["registrations"], adoptErr)
				return
			}
			if result.RegistrationID <= 0 || result.Revision <= 0 || result.AuditID < 0 || result.Inserted && result.AuditID == 0 {
				serveAdministrationServiceError(response, request, views["registrations"], errors.New("invalid registration adoption result"))
				return
			}
			destination, _ := builder.Path("admin", "registrations")
			serveMutationNavigation(response, request, destination)
		})
	}
	if services.Invitations != nil {
		loadInvitationPage := func(request *http.Request, actor auth.AccessContext) (administrationInvitationsView, error) {
			page, loadErr := services.Invitations.List(request.Context(), actor)
			if loadErr != nil {
				return administrationInvitationsView{}, loadErr
			}
			requestID, requestErr := moderationRequestUUID(request.Context())
			if requestErr != nil {
				return administrationInvitationsView{}, requestErr
			}
			reference := services.Invitations.Clock().UTC().Truncate(time.Second)
			if reference.IsZero() {
				return administrationInvitationsView{}, errors.New("invalid invitation clock")
			}
			presentation := administrationInvitationsView{Invitations: make([]administrationInvitationView, len(page.Invitations)), ActionURL: views["invitations"].CanonicalURL, CSRFToken: csrfTokenFromContext(request.Context()), IdempotencyKey: fmt.Sprintf("%x", requestID.Bytes), ExpiryReference: reference.Format(time.RFC3339), More: page.More, SMTPConfigured: services.Invitations.SMTPConfigured}
			for index, invitation := range page.Invitations {
				revokeURL := ""
				if invitation.Handle != "" {
					revokeURL, requestErr = builder.Path("admin", "invitations", invitation.Handle, "revoke")
					if requestErr != nil {
						return administrationInvitationsView{}, requestErr
					}
				}
				presentation.Invitations[index] = administrationInvitationView{Name: invitation.Name, Status: invitation.Status, Delivery: invitation.Delivery, Failure: invitation.Failure, ExpiresAt: invitation.ExpiresAt.Format(time.RFC3339), RevokeURL: revokeURL}
			}
			return presentation, nil
		}
		router.Get("/admin/invitations", func(response http.ResponseWriter, request *http.Request) {
			actor, ok := authorized(response, request)
			if !ok {
				return
			}
			presentation, loadErr := loadInvitationPage(request, actor)
			if loadErr != nil {
				serveAdministrationServiceError(response, request, views["invitations"], loadErr)
				return
			}
			render(response, request, views["invitations"], administrationInvitationsBody(presentation))
		})
		router.Post("/admin/invitations", func(response http.ResponseWriter, request *http.Request) {
			actor, ok := authorized(response, request)
			if !ok {
				return
			}
			form, ok := parseAdministrationForm(response, request, views["invitations"], maximumAdministrationSmallFormBytes, []string{"_csrf", "email", "display_name", "expires_minutes", "expires_reference", "delivery", "reason", "idempotency_key"})
			if !ok {
				return
			}
			requestID, parseErr := decodeModerationRequestID(form.Get("idempotency_key"))
			minutes, minutesErr := strconv.ParseInt(form.Get("expires_minutes"), 10, 32)
			reference, referenceErr := time.Parse(time.RFC3339, form.Get("expires_reference"))
			deliver := form.Get("delivery") == "email"
			if parseErr != nil || minutesErr != nil || referenceErr != nil || reference.Location() != time.UTC || reference.Format(time.RFC3339) != form.Get("expires_reference") ||
				minutes < 16 || minutes > 7*24*60-1 || strconv.FormatInt(minutes, 10) != form.Get("expires_minutes") || (!deliver && form.Get("delivery") != "none") || deliver && !services.Invitations.SMTPConfigured {
				renderAdministrationError(response, request, views["invitations"], http.StatusBadRequest, "Invalid form", "Check the invitation fields and try again.")
				return
			}
			presentation, loadErr := loadInvitationPage(request, actor)
			if loadErr != nil {
				serveAdministrationServiceError(response, request, views["invitations"], loadErr)
				return
			}
			result, createErr := services.Invitations.Create(request.Context(), actor, registration.InvitationInput{Email: form.Get("email"), DisplayName: form.Get("display_name"), Reason: form.Get("reason"), ExpiresAt: reference.Add(time.Duration(minutes) * time.Minute), Deliver: deliver, RequestID: requestID})
			if createErr != nil {
				serveAdministrationMutationError(response, request, views["invitations"], createErr)
				return
			}
			validDelivery := result.Delivery == "not_requested" || result.Delivery == "queued" || result.Delivery == "failed" || result.Delivery == "unknown"
			if result.Status != "active" || !result.Completed || !validDelivery || result.Revision <= 0 || result.AuditID < 0 ||
				result.TokenUUID != "" && (result.AuditID == 0 || !administrationInvitationToken.MatchString(result.TokenUUID)) {
				serveAdministrationServiceError(response, request, views["invitations"], errors.New("invalid invitation creation result"))
				return
			}
			if result.TokenUUID != "" {
				target := url.URL{Scheme: services.Invitations.Issuer.Scheme, Host: services.Invitations.Issuer.Host, Path: "/if/flow/" + services.Invitations.FlowSlug + "/", RawQuery: url.Values{"itoken": {result.TokenUUID}}.Encode()}
				presentation.OneTimeURL = target.String()
			}
			render(response, request, views["invitations"], administrationInvitationsBody(presentation))
		})
		router.Post("/admin/invitations/{handle}/revoke", func(response http.ResponseWriter, request *http.Request) {
			actor, ok := authorized(response, request)
			if !ok {
				return
			}
			handle := chi.URLParam(request, "handle")
			if !administrationAdoptionHandle.MatchString(handle) {
				renderAdministrationError(response, request, views["invitations"], http.StatusNotFound, "Invitation not found", "The requested invitation does not exist.")
				return
			}
			form, ok := parseAdministrationForm(response, request, views["invitations"], maximumAdministrationSmallFormBytes, []string{"_csrf", "reason"})
			if !ok {
				return
			}
			requestID, requestErr := moderationRequestUUID(request.Context())
			if requestErr != nil {
				serveAdministrationServiceError(response, request, views["invitations"], requestErr)
				return
			}
			if _, revokeErr := services.Invitations.Revoke(request.Context(), actor, registration.InvitationRevocationInput{Handle: handle, Reason: form.Get("reason"), RequestID: requestID}); revokeErr != nil {
				serveAdministrationMutationError(response, request, views["invitations"], revokeErr)
				return
			}
			destination, _ := builder.Path("admin", "invitations")
			serveMutationNavigation(response, request, destination)
		})
	}
	router.Get("/admin/accounts/{userID}", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		userID, _ := parseCanonicalPositiveID(chi.URLParam(request, "userID"))
		after, _ := parseAdministrationCursor(request.URL.Query(), "groups_after")
		account, loadErr := services.LoadAccount(request.Context(), actor, userID)
		if loadErr != nil {
			serveAdministrationServiceError(response, request, views["account"], loadErr)
			return
		}
		groups, loadErr := services.ListAccountGroups(request.Context(), actor, userID, after)
		if loadErr != nil {
			serveAdministrationServiceError(response, request, views["account"], loadErr)
			return
		}
		target, _ := builder.Path("admin", "accounts", strconv.FormatInt(userID, 10))
		roleAction := target + "/role"
		reconcileAction := ""
		if account.AuthentikSyncState == "removal_required" || account.AuthentikSyncState == "grant_required" {
			reconcileAction = target + "/identity/reconcile"
		}
		status := "active"
		if account.Suspended {
			status = "suspended"
		}
		presentation := administrationAccountView{DisplayName: account.DisplayName, Role: administrationRoleName(account.Role), Status: status, Revision: strconv.FormatInt(account.Revision, 10), RoleAction: roleAction, ReconcileAction: reconcileAction, AuthentikSyncState: account.AuthentikSyncState, CSRFToken: csrfTokenFromContext(request.Context()), Groups: make([]administrationMembershipView, len(groups.Groups))}
		for index, group := range groups.Groups {
			action, label := "grant", "Grant"
			if group.Member {
				action, label = "revoke", "Revoke"
			}
			groupAction, _ := builder.Path("admin", "accounts", strconv.FormatInt(userID, 10), "groups", strconv.FormatInt(group.ID, 10))
			presentation.Groups[index] = administrationMembershipView{ID: group.ID, Name: group.Name, Action: action, Label: label, ActionURL: groupAction}
		}
		if groups.NextAfter > 0 {
			presentation.NextGroupsURL, _ = builder.PathWithQuery([]string{"admin", "accounts", strconv.FormatInt(userID, 10)}, url.Values{"groups_after": {strconv.FormatInt(groups.NextAfter, 10)}})
		}
		render(response, request, views["account"], administrationAccountBody(presentation))
	})
	router.Post("/admin/accounts/{userID}/identity/reconcile", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		userID, _ := parseCanonicalPositiveID(chi.URLParam(request, "userID"))
		form, ok := parseAdministrationForm(response, request, views["account"], maximumAdministrationSmallFormBytes, []string{"_csrf", "reason"})
		if !ok {
			return
		}
		requestID, err := moderationRequestUUID(request.Context())
		if err != nil {
			serveAdministrationServiceError(response, request, views["account"], err)
			return
		}
		result, err := services.ReconcileIdentity(request.Context(), actor, userID, form.Get("reason"), requestID)
		if err != nil {
			serveAdministrationMutationError(response, request, views["account"], err)
			return
		}
		if result.UserID != userID || result.Revision <= 0 || result.AuditID <= 0 || result.SyncState != "accepted" && result.SyncState != "suspended" {
			serveAdministrationServiceError(response, request, views["account"], errors.New("invalid identity reconciliation result"))
			return
		}
		destination, _ := builder.Path("admin", "accounts", strconv.FormatInt(userID, 10))
		serveMutationNavigation(response, request, destination)
	})
	router.Post("/admin/accounts/{userID}/role", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		userID, _ := parseCanonicalPositiveID(chi.URLParam(request, "userID"))
		form, ok := parseAdministrationForm(response, request, views["account"], maximumAdministrationSmallFormBytes, []string{"_csrf", "role", "expected_role", "reason", "revision"})
		if !ok {
			return
		}
		revision, err := parsePositiveFormID(form.Get("revision"))
		if err != nil {
			renderAdministrationError(response, request, views["account"], http.StatusBadRequest, "Invalid form", "Reload the account and try again.")
			return
		}
		requestID, err := moderationRequestUUID(request.Context())
		if err != nil {
			serveAdministrationServiceError(response, request, views["account"], err)
			return
		}
		role, roleOK := parseAdministrationRole(form.Get("role"))
		expectedRole, expectedOK := parseAdministrationRole(form.Get("expected_role"))
		if !roleOK || !expectedOK {
			renderAdministrationError(response, request, views["account"], http.StatusBadRequest, "Invalid form", "Reload the account and try again.")
			return
		}
		result, err := services.ChangeRole(request.Context(), actor, userID, role, expectedRole, form.Get("reason"), revision, requestID)
		if err != nil {
			serveAdministrationMutationError(response, request, views["account"], err)
			return
		}
		if result.UserID != userID || result.Revision != revision+1 || result.AuditID <= 0 {
			serveAdministrationServiceError(response, request, views["account"], errors.New("invalid role mutation result"))
			return
		}
		destination, _ := builder.Path("admin", "accounts", strconv.FormatInt(userID, 10))
		serveMutationNavigation(response, request, destination)
	})
	router.Post("/admin/accounts/{userID}/groups/{groupID}", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		userID, _ := parseCanonicalPositiveID(chi.URLParam(request, "userID"))
		groupID, _ := parseCanonicalPositiveID(chi.URLParam(request, "groupID"))
		form, ok := parseAdministrationForm(response, request, views["account"], maximumAdministrationSmallFormBytes, []string{"_csrf", "action", "reason", "revision"})
		if !ok {
			return
		}
		revision, parseErr := parsePositiveFormID(form.Get("revision"))
		grant, actionOK := parseGrantAction(form.Get("action"))
		if parseErr != nil || !actionOK {
			renderAdministrationError(response, request, views["account"], http.StatusBadRequest, "Invalid form", "Reload the account and try again.")
			return
		}
		requestID, err := moderationRequestUUID(request.Context())
		if err != nil {
			serveAdministrationServiceError(response, request, views["account"], err)
			return
		}
		result, err := services.ChangeMembership(request.Context(), actor, userID, groupID, grant, form.Get("reason"), revision, requestID)
		if err != nil {
			serveAdministrationMutationError(response, request, views["account"], err)
			return
		}
		if result.UserID != userID || result.Revision != revision+1 || result.AuditID <= 0 {
			serveAdministrationServiceError(response, request, views["account"], errors.New("invalid membership mutation result"))
			return
		}
		destination, _ := builder.Path("admin", "accounts", strconv.FormatInt(userID, 10))
		serveMutationNavigation(response, request, destination)
	})
	router.Get("/admin/groups", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		after, _ := parseAdministrationCursor(request.URL.Query(), "after")
		page, loadErr := services.ListGroups(request.Context(), actor, after)
		if loadErr != nil {
			serveAdministrationServiceError(response, request, views["groups"], loadErr)
			return
		}
		create, _ := builder.Path("admin", "groups")
		presentation := administrationGroupsView{CreateAction: create, CSRFToken: csrfTokenFromContext(request.Context()), Groups: make([]administrationGroupItemView, len(page.Groups))}
		for index, group := range page.Groups {
			action, _ := builder.Path("admin", "groups", strconv.FormatInt(group.ID, 10))
			presentation.Groups[index] = administrationGroupItemView{Name: group.Name, Revision: strconv.FormatInt(group.Revision, 10), ActionURL: action}
		}
		if page.NextAfter > 0 {
			presentation.NextURL, _ = builder.PathWithQuery([]string{"admin", "groups"}, url.Values{"after": {strconv.FormatInt(page.NextAfter, 10)}})
		}
		render(response, request, views["groups"], administrationGroupsBody(presentation))
	})
	router.Post("/admin/groups", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		form, ok := parseAdministrationForm(response, request, views["groups"], maximumAdministrationSmallFormBytes, []string{"_csrf", "name", "reason"})
		if !ok {
			return
		}
		requestID, err := moderationRequestUUID(request.Context())
		if err != nil {
			serveAdministrationServiceError(response, request, views["groups"], err)
			return
		}
		result, err := services.CreateGroup(request.Context(), actor, form.Get("name"), form.Get("reason"), requestID)
		if err != nil {
			serveAdministrationMutationError(response, request, views["groups"], err)
			return
		}
		if result.GroupID <= 0 || result.AuditID <= 0 || result.Revision != 1 {
			serveAdministrationServiceError(response, request, views["groups"], errors.New("invalid group result"))
			return
		}
		destination, _ := builder.Path("admin", "groups")
		serveMutationNavigation(response, request, destination)
	})
	router.Post("/admin/groups/{groupID}", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		groupID, _ := parseCanonicalPositiveID(chi.URLParam(request, "groupID"))
		form, ok := parseAdministrationForm(response, request, views["groups"], maximumAdministrationSmallFormBytes, []string{"_csrf", "name", "reason", "revision"})
		if !ok {
			return
		}
		revision, err := parsePositiveFormID(form.Get("revision"))
		if err != nil {
			renderAdministrationError(response, request, views["groups"], 400, "Invalid form", "Reload groups and try again.")
			return
		}
		requestID, err := moderationRequestUUID(request.Context())
		if err != nil {
			serveAdministrationServiceError(response, request, views["groups"], err)
			return
		}
		result, err := services.RenameGroup(request.Context(), actor, groupID, form.Get("name"), form.Get("reason"), revision, requestID)
		if err != nil {
			serveAdministrationMutationError(response, request, views["groups"], err)
			return
		}
		if result.GroupID != groupID || result.Revision != revision+1 || result.AuditID <= 0 {
			serveAdministrationServiceError(response, request, views["groups"], errors.New("invalid rename result"))
			return
		}
		destination, _ := builder.Path("admin", "groups")
		serveMutationNavigation(response, request, destination)
	})
	router.Get("/admin/areas", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		afterOrder, afterID, _ := parseAdministrationAreaCursor(request.URL.Query())
		page, err := services.ListAreas(request.Context(), actor, afterOrder, afterID)
		if err != nil {
			serveAdministrationServiceError(response, request, views["areas"], err)
			return
		}
		create, _ := builder.Path("admin", "areas")
		presentation := administrationAreasView{CreateAction: create, CSRFToken: csrfTokenFromContext(request.Context()), Areas: make([]administrationAreaListItem, len(page.Areas))}
		for index, area := range page.Areas {
			target, _ := builder.Path("admin", "areas", strconv.FormatInt(area.ID, 10))
			presentation.Areas[index] = administrationAreaListItem{Name: area.Name, Slug: area.Slug, Visibility: string(area.Visibility), PostingMode: string(area.PostingMode), URL: target, GroupCount: area.GroupCount}
		}
		if page.NextAfterID > 0 {
			presentation.NextURL, _ = builder.PathWithQuery([]string{"admin", "areas"}, url.Values{"after_id": {strconv.FormatInt(page.NextAfterID, 10)}, "after_order": {strconv.FormatInt(int64(page.NextAfterOrder), 10)}})
		}
		render(response, request, views["areas"], administrationAreasBody(presentation))
	})
	router.Post("/admin/areas", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		input, ok := parseAdministrationAreaForm(response, request, views["areas"], true)
		if !ok {
			return
		}
		requestID, err := moderationRequestUUID(request.Context())
		if err != nil {
			serveAdministrationServiceError(response, request, views["areas"], err)
			return
		}
		result, err := services.CreateArea(request.Context(), actor, input, requestID)
		if err != nil {
			serveAdministrationMutationError(response, request, views["areas"], err)
			return
		}
		if result.AreaID <= 0 || result.Revision != 1 || result.AuditID <= 0 {
			serveAdministrationServiceError(response, request, views["areas"], errors.New("invalid create area result"))
			return
		}
		destination, _ := builder.Path("admin", "areas", strconv.FormatInt(result.AreaID, 10))
		serveMutationNavigation(response, request, destination)
	})
	router.Get("/admin/areas/{areaID}", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		areaID, _ := parseCanonicalPositiveID(chi.URLParam(request, "areaID"))
		after, _ := parseAdministrationCursor(request.URL.Query(), "groups_after")
		detail, err := services.LoadArea(request.Context(), actor, areaID, after)
		if err != nil {
			serveAdministrationServiceError(response, request, views["area"], err)
			return
		}
		target, _ := builder.Path("admin", "areas", strconv.FormatInt(areaID, 10))
		presentation := administrationAreaView{Name: detail.Area.Name, Slug: detail.Area.Slug, Description: detail.Area.Description, DisplayOrder: strconv.FormatInt(int64(detail.Area.DisplayOrder), 10), Visibility: string(detail.Area.Visibility), PostingMode: string(detail.Area.PostingMode), Revision: strconv.FormatInt(detail.Area.Revision, 10), ActionURL: target, CSRFToken: csrfTokenFromContext(request.Context()), Groups: make([]administrationMembershipView, len(detail.Groups))}
		for index, group := range detail.Groups {
			action, label := "grant", "Grant"
			if group.Assigned {
				action, label = "revoke", "Revoke"
			}
			groupAction, _ := builder.Path("admin", "areas", strconv.FormatInt(areaID, 10), "groups", strconv.FormatInt(group.ID, 10))
			presentation.Groups[index] = administrationMembershipView{ID: group.ID, Name: group.Name, Action: action, Label: label, ActionURL: groupAction}
		}
		if detail.NextAfter > 0 {
			presentation.NextGroupsURL, _ = builder.PathWithQuery([]string{"admin", "areas", strconv.FormatInt(areaID, 10)}, url.Values{"groups_after": {strconv.FormatInt(detail.NextAfter, 10)}})
		}
		render(response, request, views["area"], administrationAreaBody(presentation))
	})
	router.Post("/admin/areas/{areaID}", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		areaID, _ := parseCanonicalPositiveID(chi.URLParam(request, "areaID"))
		input, ok := parseAdministrationAreaForm(response, request, views["area"], false)
		if !ok {
			return
		}
		requestID, err := moderationRequestUUID(request.Context())
		if err != nil {
			serveAdministrationServiceError(response, request, views["area"], err)
			return
		}
		result, err := services.UpdateArea(request.Context(), actor, areaID, input, requestID)
		if err != nil {
			serveAdministrationMutationError(response, request, views["area"], err)
			return
		}
		if result.AreaID != areaID || result.Revision != input.Revision+1 || result.AuditID <= 0 {
			serveAdministrationServiceError(response, request, views["area"], errors.New("invalid update area result"))
			return
		}
		destination, _ := builder.Path("admin", "areas", strconv.FormatInt(areaID, 10))
		serveMutationNavigation(response, request, destination)
	})
	router.Post("/admin/areas/{areaID}/groups/{groupID}", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		areaID, _ := parseCanonicalPositiveID(chi.URLParam(request, "areaID"))
		groupID, _ := parseCanonicalPositiveID(chi.URLParam(request, "groupID"))
		form, ok := parseAdministrationForm(response, request, views["area"], maximumAdministrationSmallFormBytes, []string{"_csrf", "action", "reason", "revision"})
		if !ok {
			return
		}
		revision, parseErr := parsePositiveFormID(form.Get("revision"))
		grant, valid := parseGrantAction(form.Get("action"))
		if parseErr != nil || !valid {
			renderAdministrationError(response, request, views["area"], 400, "Invalid form", "Reload the area and try again.")
			return
		}
		requestID, err := moderationRequestUUID(request.Context())
		if err != nil {
			serveAdministrationServiceError(response, request, views["area"], err)
			return
		}
		result, err := services.ChangeAreaGroup(request.Context(), actor, areaID, groupID, grant, form.Get("reason"), revision, requestID)
		if err != nil {
			serveAdministrationMutationError(response, request, views["area"], err)
			return
		}
		if result.AreaID != areaID || result.Revision != revision+1 || result.AuditID <= 0 {
			serveAdministrationServiceError(response, request, views["area"], errors.New("invalid area group result"))
			return
		}
		destination, _ := builder.Path("admin", "areas", strconv.FormatInt(areaID, 10))
		serveMutationNavigation(response, request, destination)
	})
	return recordRoutePattern(router), nil
}

func parseAdministrationForm(response http.ResponseWriter, request *http.Request, view pageView, limit int64, fields []string) (url.Values, bool) {
	if err := validateCSRFRequest(request, limit); err != nil {
		renderAdministrationError(response, request, view, http.StatusForbidden, "Request verification failed", "Reload the administration page and try again.")
		return nil, false
	}
	mediaType, parameters, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" || len(parameters) != 0 {
		renderAdministrationError(response, request, view, http.StatusBadRequest, "Invalid form", "The form content type is invalid.")
		return nil, false
	}
	if request.ContentLength > limit {
		renderAdministrationError(response, request, view, http.StatusBadRequest, "Invalid form", "The form body is too large.")
		return nil, false
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	if err := request.ParseForm(); err != nil {
		renderAdministrationError(response, request, view, http.StatusBadRequest, "Invalid form", "Reload the administration page and try again.")
		return nil, false
	}
	allowed := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	if len(request.PostForm) != len(fields) {
		renderAdministrationError(response, request, view, http.StatusBadRequest, "Invalid form", "The form fields are incomplete or unexpected.")
		return nil, false
	}
	for key, values := range request.PostForm {
		if _, ok := allowed[key]; !ok || len(values) != 1 {
			renderAdministrationError(response, request, view, http.StatusBadRequest, "Invalid form", "The form fields are incomplete or unexpected.")
			return nil, false
		}
	}
	for _, field := range fields {
		if len(request.PostForm[field]) != 1 {
			renderAdministrationError(response, request, view, http.StatusBadRequest, "Invalid form", "The form fields are incomplete or unexpected.")
			return nil, false
		}
	}
	return request.PostForm, true
}

func parseAdministrationAreaForm(response http.ResponseWriter, request *http.Request, view pageView, create bool) (administration.AreaCoreInput, bool) {
	fields := []string{"_csrf", "name", "description", "display_order", "visibility", "posting_mode", "initial_group_id", "reason"}
	if create {
		fields = append(fields, "slug")
	}
	if !create {
		fields = append(fields, "revision")
	}
	form, ok := parseAdministrationForm(response, request, view, maximumAdministrationAreaFormBytes, fields)
	if !ok {
		return administration.AreaCoreInput{}, false
	}
	order, err := strconv.ParseInt(form.Get("display_order"), 10, 32)
	if err != nil || order < 0 {
		renderAdministrationError(response, request, view, 400, "Invalid form", "The area fields are invalid.")
		return administration.AreaCoreInput{}, false
	}
	initial := int64(0)
	if form.Get("initial_group_id") != "" {
		initial, err = parsePositiveFormID(form.Get("initial_group_id"))
		if err != nil {
			renderAdministrationError(response, request, view, 400, "Invalid form", "The initial group is invalid.")
			return administration.AreaCoreInput{}, false
		}
	}
	revision := int64(0)
	if !create {
		revision, err = parsePositiveFormID(form.Get("revision"))
		if err != nil {
			renderAdministrationError(response, request, view, 400, "Invalid form", "The area revision is invalid.")
			return administration.AreaCoreInput{}, false
		}
	}
	return administration.AreaCoreInput{Slug: form.Get("slug"), Name: form.Get("name"), Description: form.Get("description"), DisplayOrder: int32(order), Visibility: policy.Visibility(form.Get("visibility")), PostingMode: policy.PostingMode(form.Get("posting_mode")), InitialGroupID: initial, Reason: form.Get("reason"), Revision: revision}, true
}

func parseAdministrationAreaCursor(values url.Values) (int32, int64, error) {
	if len(values) == 0 {
		return 0, 0, nil
	}
	if len(values) != 2 || len(values["after_order"]) != 1 || len(values["after_id"]) != 1 {
		return 0, 0, fmt.Errorf("invalid area cursor")
	}
	orderText := values.Get("after_order")
	order, err := strconv.ParseInt(orderText, 10, 32)
	if err != nil || order < 0 || strconv.FormatInt(order, 10) != orderText {
		return 0, 0, fmt.Errorf("invalid area order cursor")
	}
	id, err := parsePositiveFormID(values.Get("after_id"))
	if err != nil {
		return 0, 0, err
	}
	return int32(order), id, nil
}

func parseAdministrationCursor(values url.Values, key string) (int64, error) {
	if len(values) == 0 {
		return 0, nil
	}
	if len(values) != 1 || len(values[key]) != 1 {
		return 0, fmt.Errorf("invalid cursor")
	}
	return parsePositiveFormID(values.Get(key))
}
func parsePositiveFormID(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, fmt.Errorf("invalid identifier")
	}
	return parsed, nil
}
func parseGrantAction(action string) (bool, bool) {
	switch action {
	case "grant":
		return true, true
	case "revoke":
		return false, true
	default:
		return false, false
	}
}

func parseAdministrationRole(value string) (policy.Role, bool) {
	switch value {
	case "member":
		return policy.RoleMember, true
	case "moderator":
		return policy.RoleModerator, true
	case "administrator":
		return policy.RoleAdministrator, true
	default:
		return 0, false
	}
}

func administrationRoleName(role policy.Role) string {
	switch role {
	case policy.RoleMember:
		return "member"
	case policy.RoleModerator:
		return "moderator"
	case policy.RoleAdministrator:
		return "administrator"
	default:
		return ""
	}
}

func renderAdministrationError(response http.ResponseWriter, request *http.Request, view pageView, status int, heading, message string) {
	view.Title = heading
	view.CanonicalURL = ""
	if err := renderResponse(response, request, status, errorPage(view, status, heading, message), errorContent(view, status, heading, message)); err != nil {
		panic(err)
	}
}
func serveAdministrationServiceError(response http.ResponseWriter, request *http.Request, view pageView, err error) {
	switch {
	case errors.Is(err, administration.ErrAdministrationDenied), errors.Is(err, administration.ErrAccountAdministrationDenied), errors.Is(err, registration.ErrDenied):
		renderAdministrationError(response, request, view, 403, "Administration denied", "Your current account cannot administer this board.")
	case errors.Is(err, administration.ErrAdministrationNotFound), errors.Is(err, administration.ErrAccountAdministrationNotFound):
		renderAdministrationError(response, request, view, 404, "Page not found", "The requested administration target does not exist.")
	default:
		renderAdministrationError(response, request, view, 503, "Administration unavailable", "Administration is temporarily unavailable.")
	}
}
func serveAdministrationMutationError(response http.ResponseWriter, request *http.Request, view pageView, err error) {
	switch {
	case errors.Is(err, administration.ErrAdministrationInput), errors.Is(err, administration.ErrAccountAdministrationInput), errors.Is(err, registration.ErrInput):
		renderAdministrationError(response, request, view, 422, "Invalid change", "Check every field and try again.")
	case errors.Is(err, administration.ErrAdministrationConflict), errors.Is(err, administration.ErrAccountAdministrationConflict), errors.Is(err, administration.ErrAccountAdministratorContinuity), errors.Is(err, registration.ErrConflict):
		renderAdministrationError(response, request, view, 409, "Change conflict", "The target changed, the change was a no-op, or administrator continuity would be lost. Reload and try again.")
	default:
		serveAdministrationServiceError(response, request, view, err)
	}
}

func administrationRouteValid(request *http.Request) bool {
	if request.URL.RawPath != "" || request.URL.ForceQuery {
		return false
	}
	path := request.URL.Path
	method := request.Method
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return false
	}
	if path == "/admin" {
		return method == http.MethodGet && len(query) == 0
	}
	if path == "/admin/accounts" || path == "/admin/groups" || path == "/admin/areas" || path == "/admin/registrations" || path == "/admin/invitations" {
		if method == http.MethodGet {
			if path == "/admin/areas" {
				_, _, err := parseAdministrationAreaCursor(query)
				return err == nil
			}
			_, err := parseAdministrationCursor(query, "after")
			return err == nil
		}
		return path != "/admin/registrations" && method == http.MethodPost && len(query) == 0
	}
	parts := strings.Split(strings.TrimPrefix(path, "/admin/"), "/")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	if parts[0] != "accounts" && parts[0] != "groups" && parts[0] != "areas" && parts[0] != "registrations" && parts[0] != "invitations" {
		return false
	}
	adoption := parts[0] == "registrations" && len(parts) == 3 && parts[2] == "adopt"
	revocation := parts[0] == "invitations" && len(parts) == 3 && parts[2] == "revoke"
	if adoption || revocation {
		if !administrationAdoptionHandle.MatchString(parts[1]) {
			return false
		}
	} else if _, err := parseCanonicalPositiveID(parts[1]); err != nil {
		return false
	}
	if method == http.MethodGet {
		return len(parts) == 2 && (parts[0] == "accounts" || parts[0] == "areas") && func() bool { _, err := parseAdministrationCursor(query, "groups_after"); return err == nil }()
	}
	if method != http.MethodPost || len(query) != 0 {
		return false
	}
	if len(parts) == 2 {
		return parts[0] == "groups" || parts[0] == "areas"
	}
	if len(parts) == 3 {
		return parts[0] == "accounts" && parts[2] == "role" || parts[0] == "registrations" && (parts[2] == "approve" || parts[2] == "reject" || parts[2] == "adopt") || revocation
	}
	if parts[0] == "accounts" && parts[2] == "identity" && parts[3] == "reconcile" {
		return true
	}
	if parts[2] != "groups" || (parts[0] != "accounts" && parts[0] != "areas") {
		return false
	}
	_, err = parseCanonicalPositiveID(parts[3])
	return err == nil
}

func withAdministrationPreflight(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !administrationRouteValid(request) {
			request.Pattern = "* /admin"
			status := http.StatusNotFound
			if strings.HasPrefix(request.URL.Path, "/admin") && (request.Method != http.MethodGet && request.Method != http.MethodPost) {
				status = http.StatusMethodNotAllowed
			}
			if err := renderUnbrandedRouteError(response, status); err != nil {
				panic(err)
			}
			return
		}
		next.ServeHTTP(response, request)
	})
}
