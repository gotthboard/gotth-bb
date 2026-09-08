package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"
	"github.com/gotthboard/gotth-bb/internal/administration"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/policy"
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
	ListAreas         func(context.Context, auth.AccessContext, int64) (administration.AreaPage, error)
	LoadArea          func(context.Context, auth.AccessContext, int64, int64) (administration.AreaDetail, error)
	CreateArea        func(context.Context, auth.AccessContext, administration.AreaCoreInput, pgtype.UUID) (administration.AreaCompletionResult, error)
	UpdateArea        func(context.Context, auth.AccessContext, int64, administration.AreaCoreInput, pgtype.UUID) (administration.AreaCompletionResult, error)
	ChangeAreaGroup   func(context.Context, auth.AccessContext, int64, int64, bool, string, int64, pgtype.UUID) (administration.AreaCompletionResult, error)
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
	ID                  int64
	Name, Action, Label string
}
type administrationAccountView struct {
	DisplayName, Role, Status, Revision, RoleAction, MembershipAction, CSRFToken, NextGroupsURL string
	Groups                                                                                      []administrationMembershipView
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
	Name, Slug, Description, DisplayOrder, Visibility, PostingMode, Revision, ActionURL, GroupActionURL, CSRFToken, NextGroupsURL string
	Groups                                                                                                                        []administrationMembershipView
}

func validAdministrationHTTPServices(services AdministrationHTTPServices) bool {
	return services.Dashboard != nil && services.ListAccounts != nil && services.LoadAccount != nil && services.ListAccountGroups != nil && services.ListGroups != nil &&
		services.CreateGroup != nil && services.RenameGroup != nil && services.ChangeMembership != nil && services.ChangeRole != nil &&
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
		membershipAction := target + "/memberships"
		status := "active"
		if account.Suspended {
			status = "suspended"
		}
		presentation := administrationAccountView{DisplayName: account.DisplayName, Role: administrationRoleName(account.Role), Status: status, Revision: strconv.FormatInt(account.Revision, 10), RoleAction: roleAction, MembershipAction: membershipAction, CSRFToken: csrfTokenFromContext(request.Context()), Groups: make([]administrationMembershipView, len(groups.Groups))}
		for index, group := range groups.Groups {
			action, label := "grant", "Grant"
			if group.Member {
				action, label = "revoke", "Revoke"
			}
			presentation.Groups[index] = administrationMembershipView{ID: group.ID, Name: group.Name, Action: action, Label: label}
		}
		if groups.NextAfter > 0 {
			presentation.NextGroupsURL, _ = builder.PathWithQuery([]string{"admin", "accounts", strconv.FormatInt(userID, 10)}, url.Values{"groups_after": {strconv.FormatInt(groups.NextAfter, 10)}})
		}
		render(response, request, views["account"], administrationAccountBody(presentation))
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
	router.Post("/admin/accounts/{userID}/memberships", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		userID, _ := parseCanonicalPositiveID(chi.URLParam(request, "userID"))
		form, ok := parseAdministrationForm(response, request, views["account"], maximumAdministrationSmallFormBytes, []string{"_csrf", "action", "group_id", "reason", "revision"})
		if !ok {
			return
		}
		groupID, errorOne := parsePositiveFormID(form.Get("group_id"))
		revision, errorTwo := parsePositiveFormID(form.Get("revision"))
		grant, actionOK := parseGrantAction(form.Get("action"))
		if errorOne != nil || errorTwo != nil || !actionOK {
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
		after, _ := parseAdministrationCursor(request.URL.Query(), "after")
		page, err := services.ListAreas(request.Context(), actor, after)
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
		if page.NextAfter > 0 {
			presentation.NextURL, _ = builder.PathWithQuery([]string{"admin", "areas"}, url.Values{"after": {strconv.FormatInt(page.NextAfter, 10)}})
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
		presentation := administrationAreaView{Name: detail.Area.Name, Slug: detail.Area.Slug, Description: detail.Area.Description, DisplayOrder: strconv.FormatInt(int64(detail.Area.DisplayOrder), 10), Visibility: string(detail.Area.Visibility), PostingMode: string(detail.Area.PostingMode), Revision: strconv.FormatInt(detail.Area.Revision, 10), ActionURL: target, GroupActionURL: target + "/groups", CSRFToken: csrfTokenFromContext(request.Context()), Groups: make([]administrationMembershipView, len(detail.Groups))}
		for index, group := range detail.Groups {
			action, label := "grant", "Grant"
			if group.Assigned {
				action, label = "revoke", "Revoke"
			}
			presentation.Groups[index] = administrationMembershipView{ID: group.ID, Name: group.Name, Action: action, Label: label}
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
	router.Post("/admin/areas/{areaID}/groups", func(response http.ResponseWriter, request *http.Request) {
		actor, ok := authorized(response, request)
		if !ok {
			return
		}
		areaID, _ := parseCanonicalPositiveID(chi.URLParam(request, "areaID"))
		form, ok := parseAdministrationForm(response, request, views["area"], maximumAdministrationSmallFormBytes, []string{"_csrf", "action", "group_id", "reason", "revision"})
		if !ok {
			return
		}
		groupID, e1 := parsePositiveFormID(form.Get("group_id"))
		revision, e2 := parsePositiveFormID(form.Get("revision"))
		grant, valid := parseGrantAction(form.Get("action"))
		if e1 != nil || e2 != nil || !valid {
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
	fields := []string{"_csrf", "slug", "name", "description", "display_order", "visibility", "posting_mode", "initial_group_id", "reason"}
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
	case errors.Is(err, administration.ErrAdministrationDenied), errors.Is(err, administration.ErrAccountAdministrationDenied):
		renderAdministrationError(response, request, view, 403, "Administration denied", "Your current account cannot administer this board.")
	case errors.Is(err, administration.ErrAdministrationNotFound), errors.Is(err, administration.ErrAccountAdministrationNotFound):
		renderAdministrationError(response, request, view, 404, "Page not found", "The requested administration target does not exist.")
	default:
		renderAdministrationError(response, request, view, 503, "Administration unavailable", "Administration is temporarily unavailable.")
	}
}
func serveAdministrationMutationError(response http.ResponseWriter, request *http.Request, view pageView, err error) {
	switch {
	case errors.Is(err, administration.ErrAdministrationInput), errors.Is(err, administration.ErrAccountAdministrationInput):
		renderAdministrationError(response, request, view, 422, "Invalid change", "Check every field and try again.")
	case errors.Is(err, administration.ErrAdministrationConflict), errors.Is(err, administration.ErrAccountAdministrationConflict), errors.Is(err, administration.ErrAccountAdministratorContinuity):
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
	query := request.URL.Query()
	if path == "/admin" {
		return method == http.MethodGet && len(query) == 0
	}
	if path == "/admin/accounts" || path == "/admin/groups" || path == "/admin/areas" {
		if method == http.MethodGet {
			_, err := parseAdministrationCursor(query, "after")
			return err == nil
		}
		return method == http.MethodPost && len(query) == 0
	}
	parts := strings.Split(strings.TrimPrefix(path, "/admin/"), "/")
	if len(parts) < 2 || len(parts) > 3 {
		return false
	}
	if parts[0] != "accounts" && parts[0] != "groups" && parts[0] != "areas" {
		return false
	}
	if _, err := parseCanonicalPositiveID(parts[1]); err != nil {
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
	return parts[0] == "accounts" && (parts[2] == "role" || parts[2] == "memberships") || parts[0] == "areas" && parts[2] == "groups"
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
