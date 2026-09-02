package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/administration"
	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const maximumAreaAdministrationFormBytes = 16_384

type AreaAdministrationLoader func(context.Context, auth.AccessContext) (administration.AreaManagementPage, error)
type AreaCreator func(context.Context, auth.AccessContext, administration.AreaInput, pgtype.UUID) (administration.AreaMutationResult, error)
type AreaUpdater func(context.Context, auth.AccessContext, int64, administration.AreaInput, pgtype.UUID) (administration.AreaMutationResult, error)

func newAreaAdministrationHandler(builder URLBuilder, load AreaAdministrationLoader, create AreaCreator, update AreaUpdater) (http.Handler, error) {
	if load == nil || create == nil || update == nil {
		return nil, fmt.Errorf("area administration services are required")
	}
	adminURL, err := builder.Path("admin", "areas")
	if err != nil {
		return nil, fmt.Errorf("build area administration URL: %w", err)
	}
	loginURL, err := builder.PathWithQuery([]string{"login"}, url.Values{"return": {adminURL}})
	if err != nil {
		return nil, fmt.Errorf("build area administration login URL: %w", err)
	}
	revalidationURL, err := builder.PathWithQuery([]string{"auth", "revalidate"}, url.Values{"return": {adminURL}})
	if err != nil {
		return nil, fmt.Errorf("build area administration revalidation URL: %w", err)
	}
	baseView, err := newPageView(builder, "Manage areas", "admin", "areas")
	if err != nil {
		return nil, fmt.Errorf("construct area administration view: %w", err)
	}
	serveError := func(response http.ResponseWriter, request *http.Request, status int, heading, message string) {
		view := baseView
		view.Title = heading
		view.CanonicalURL = ""
		if renderErr := renderResponse(response, request, status, errorPage(view, status, heading, message), errorContent(view, status, heading, message)); renderErr != nil {
			panic(renderErr)
		}
	}
	authorized := func(request *http.Request) (auth.AccessContext, string, bool) {
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
			return auth.AccessContext{}, loginURL, false
		}
		if authentication.RequiresRevalidation {
			return auth.AccessContext{}, revalidationURL, false
		}
		if !policy.CanAdminister(authentication.Access) {
			return authentication.Access, "", false
		}
		return authentication.Access, "", true
	}
	renderPage := func(response http.ResponseWriter, request *http.Request, status int, actor auth.AccessContext, formError string) {
		page, loadErr := load(request.Context(), actor)
		if loadErr != nil {
			if errors.Is(loadErr, administration.ErrAreaAdministrationDenied) {
				serveError(response, request, http.StatusForbidden, "Administration denied", "Your current account cannot administer areas.")
				return
			}
			serveError(response, request, http.StatusServiceUnavailable, "Area administration unavailable", "Area administration is temporarily unavailable.")
			return
		}
		presentation, buildErr := areaAdministrationPresentation(builder, page, csrfTokenFromContext(request.Context()), adminURL, formError)
		if buildErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, "Area administration unavailable", "Area administration is temporarily unavailable.")
			return
		}
		if renderErr := renderResponse(response, request, status, areaAdministrationPage(baseView, presentation), areaAdministrationContent(baseView, presentation)); renderErr != nil {
			panic(renderErr)
		}
	}

	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Get("/admin/areas", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		actor, redirect, allowed := authorized(request)
		if redirect != "" {
			serveSessionRedirect(response, request, redirect)
			return
		}
		if !allowed {
			serveError(response, request, http.StatusForbidden, "Administration denied", "Your current account cannot administer areas.")
			return
		}
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
			serveError(response, request, http.StatusNotFound, "Page not found", "The requested administration page does not exist.")
			return
		}
		renderPage(response, request, http.StatusOK, actor, "")
	})
	mutate := func(updateExisting bool) http.HandlerFunc {
		return func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set("Cache-Control", "no-store")
			actor, redirect, allowed := authorized(request)
			if redirect != "" {
				serveSessionRedirect(response, request, redirect)
				return
			}
			if !allowed {
				serveError(response, request, http.StatusForbidden, "Administration denied", "Your current account cannot administer areas.")
				return
			}
			if request.URL.RawPath != "" || request.URL.RawQuery != "" || request.URL.ForceQuery {
				serveError(response, request, http.StatusNotFound, "Page not found", "The requested administration action does not exist.")
				return
			}
			areaID := int64(0)
			if updateExisting {
				parsed, parseErr := parseCanonicalPositiveID(chi.URLParam(request, "areaID"))
				if parseErr != nil {
					serveError(response, request, http.StatusNotFound, "Page not found", "The requested area does not exist.")
					return
				}
				areaID = parsed
			}
			if csrfErr := validateCSRFRequest(request, maximumAreaAdministrationFormBytes); csrfErr != nil {
				serveError(response, request, http.StatusForbidden, "Request verification failed", "Reload area administration and try again.")
				return
			}
			input, formErr := parseAreaAdministrationForm(request, updateExisting)
			if formErr != nil {
				renderPage(response, request, http.StatusBadRequest, actor, "The area form is invalid. Reload it and try again.")
				return
			}
			requestID, requestIDErr := moderationRequestUUID(request.Context())
			if requestIDErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Area administration unavailable", "Area administration is temporarily unavailable.")
				return
			}
			var result administration.AreaMutationResult
			var mutationErr error
			if updateExisting {
				result, mutationErr = update(request.Context(), actor, areaID, input, requestID)
			} else {
				result, mutationErr = create(request.Context(), actor, input, requestID)
			}
			if mutationErr != nil {
				switch {
				case errors.Is(mutationErr, administration.ErrAreaAdministrationInput):
					renderPage(response, request, http.StatusUnprocessableEntity, actor, "Check every area field. Group-restricted areas need at least one valid group, and the audit reason must be a single line.")
				case errors.Is(mutationErr, administration.ErrAreaAdministrationDenied):
					serveError(response, request, http.StatusForbidden, "Administration denied", "Your current account cannot administer areas.")
				case errors.Is(mutationErr, administration.ErrAreaAdministrationConflict):
					renderPage(response, request, http.StatusConflict, actor, "That slug already exists, the area did not change, or its state changed. Reload and try again.")
				case errors.Is(mutationErr, pgx.ErrNoRows):
					serveError(response, request, http.StatusNotFound, "Page not found", "The requested area does not exist.")
				default:
					serveError(response, request, http.StatusServiceUnavailable, "Area administration unavailable", "Area administration is temporarily unavailable.")
				}
				return
			}
			if result.AreaID <= 0 || result.AuditID <= 0 || result.Slug != input.Slug || updateExisting && result.AreaID != areaID {
				serveError(response, request, http.StatusServiceUnavailable, "Area administration unavailable", "Area administration is temporarily unavailable.")
				return
			}
			serveMutationNavigation(response, request, adminURL)
		}
	}
	router.Post("/admin/areas", mutate(false))
	router.Post("/admin/areas/{areaID}", mutate(true))
	return recordRoutePattern(router), nil
}

func parseAreaAdministrationForm(request *http.Request, updateExisting bool) (administration.AreaInput, error) {
	if err := request.ParseForm(); err != nil {
		return administration.AreaInput{}, fmt.Errorf("parse area administration form: %w", err)
	}
	allowed := map[string]bool{"_csrf": true, "slug": true, "name": true, "description": true, "display_order": true, "visibility": true, "posting_mode": true, "group_id": true, "reason": true, "revision": true}
	for key, values := range request.PostForm {
		if !allowed[key] || key != "group_id" && len(values) != 1 {
			return administration.AreaInput{}, fmt.Errorf("area administration form field is missing, duplicated, or unknown")
		}
	}
	for _, required := range []string{"_csrf", "slug", "name", "description", "display_order", "visibility", "posting_mode", "reason"} {
		if len(request.PostForm[required]) != 1 {
			return administration.AreaInput{}, fmt.Errorf("area administration form field is missing, duplicated, or unknown")
		}
	}
	revision := time.Time{}
	if updateExisting {
		if len(request.PostForm["revision"]) != 1 {
			return administration.AreaInput{}, fmt.Errorf("area revision is missing")
		}
		parsedRevision, parseErr := time.Parse(time.RFC3339Nano, request.PostForm.Get("revision"))
		if parseErr != nil {
			return administration.AreaInput{}, fmt.Errorf("area revision is invalid")
		}
		revision = parsedRevision.UTC()
	} else if len(request.PostForm["revision"]) != 0 {
		return administration.AreaInput{}, fmt.Errorf("area revision is unexpected")
	}
	order, err := strconv.ParseInt(request.PostForm.Get("display_order"), 10, 32)
	if err != nil || order < 0 {
		return administration.AreaInput{}, fmt.Errorf("area display order is invalid")
	}
	groupIDs := make([]int64, len(request.PostForm["group_id"]))
	for index, value := range request.PostForm["group_id"] {
		groupID, parseErr := parseCanonicalPositiveID(value)
		if parseErr != nil {
			return administration.AreaInput{}, fmt.Errorf("area group is invalid")
		}
		groupIDs[index] = groupID
	}
	slices.Sort(groupIDs)
	return administration.AreaInput{
		Slug: request.PostForm.Get("slug"), Name: request.PostForm.Get("name"), Description: request.PostForm.Get("description"),
		DisplayOrder: int32(order), Visibility: policy.Visibility(request.PostForm.Get("visibility")),
		PostingMode: policy.PostingMode(request.PostForm.Get("posting_mode")), GroupIDs: groupIDs, Reason: request.PostForm.Get("reason"), Revision: revision,
	}, nil
}

func areaAdministrationPresentation(builder URLBuilder, page administration.AreaManagementPage, csrfToken, actionURL, formError string) (areaAdministrationPageView, error) {
	if len(csrfToken) != sessionCookieEncodedBytes {
		return areaAdministrationPageView{}, fmt.Errorf("area administration CSRF token is invalid")
	}
	view := areaAdministrationPageView{ActionURL: actionURL, CSRFToken: csrfToken, Areas: make([]areaAdministrationFormView, len(page.Areas)), Groups: make([]areaAdministrationGroupView, len(page.Groups)), FormError: formError}
	for index, group := range page.Groups {
		if group.ID <= 0 || group.Name == "" {
			return areaAdministrationPageView{}, fmt.Errorf("area administration group is invalid")
		}
		view.Groups[index] = areaAdministrationGroupView{ID: group.ID, Name: group.Name}
	}
	for index, area := range page.Areas {
		if area.ID <= 0 || !policy.ValidAreaSlug(area.Slug) || area.Name == "" {
			return areaAdministrationPageView{}, fmt.Errorf("area administration area is invalid")
		}
		action, err := builder.Path("admin", "areas", strconv.FormatInt(area.ID, 10))
		if err != nil {
			return areaAdministrationPageView{}, fmt.Errorf("build area update URL: %w", err)
		}
		view.Areas[index] = areaAdministrationFormView{ID: area.ID, ActionURL: action, Slug: area.Slug, Name: area.Name, Description: area.Description, DisplayOrder: strconv.FormatInt(int64(area.DisplayOrder), 10), Visibility: string(area.Visibility), PostingMode: string(area.PostingMode), GroupIDs: slices.Clone(area.GroupIDs), Revision: area.UpdatedAt.Format(time.RFC3339Nano), Editing: true}
	}
	return view, nil
}

func parseCanonicalPositiveID(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, fmt.Errorf("identifier is invalid")
	}
	return parsed, nil
}
