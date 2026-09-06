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

	"github.com/go-chi/chi/v5"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/moderation"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	maximumReportFormBytes = 16_384
	reportSubmittedQuery   = "report=submitted"
)

type reportFormsContextKey struct{}

func reportFormsEnabled(ctx context.Context) bool {
	enabled, _ := ctx.Value(reportFormsContextKey{}).(bool)
	return enabled
}

type ReportCreator func(context.Context, auth.AccessContext, moderation.ReportTargetType, int64, string) (moderation.ReportResult, error)
type ModerationReportLister func(context.Context, auth.AccessContext, int32) (store.ModerationReportPage, error)
type ModerationReportLoader func(context.Context, auth.AccessContext, int64) (store.ModerationReportDetail, error)
type ModerationReportProcessor func(context.Context, auth.AccessContext, int64, moderation.ReportAction, string, pgtype.UUID) (moderation.ReportActionResult, error)
type ExtendedModerationProcessor func(context.Context, auth.AccessContext, moderation.ExtendedActionInput, pgtype.UUID) (moderation.ExtendedActionResult, error)

type ReportHTTPServices struct {
	Create   ReportCreator
	List     ModerationReportLister
	Load     ModerationReportLoader
	Process  ModerationReportProcessor
	Extended ExtendedModerationProcessor
}

func newReportHandler(builder URLBuilder, services ReportHTTPServices) (http.Handler, error) {
	if services.Create == nil || services.List == nil || services.Load == nil || services.Process == nil || services.Extended == nil {
		return nil, fmt.Errorf("report browser services are required")
	}
	loginURL, err := builder.Path("login")
	if err != nil {
		return nil, err
	}
	revalidationURL, err := builder.Path("auth", "revalidate")
	if err != nil {
		return nil, err
	}
	homeURL, err := builder.Path()
	if err != nil {
		return nil, err
	}
	baseView, err := newPageView(builder, "Moderation reports", "moderation", "reports")
	if err != nil {
		return nil, err
	}
	serveError := func(response http.ResponseWriter, request *http.Request, status int, heading, message string) {
		view := baseView
		view.Title, view.CanonicalURL = heading, ""
		if renderErr := renderResponse(
			response,
			request,
			status,
			errorPage(view, status, heading, message),
			errorContent(view, status, heading, message),
		); renderErr != nil {
			panic(renderErr)
		}
	}
	authorized := func(request *http.Request, staff bool) (auth.AccessContext, string) {
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
			return auth.AccessContext{}, loginURL
		}
		if authentication.RequiresRevalidation {
			return auth.AccessContext{}, revalidationURL
		}
		staffDenied := staff && authentication.Access.Role != auth.RoleModerator &&
			authentication.Access.Role != auth.RoleAdministrator
		if authentication.Access.Suspended || staffDenied {
			return auth.AccessContext{}, "denied"
		}
		return authentication.Access, ""
	}
	create := func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request, false)
		if redirect == loginURL || redirect == revalidationURL {
			serveSessionRedirect(response, request, redirect)
			return
		}
		if redirect != "" {
			serveError(response, request, http.StatusForbidden, "Report denied", "Your current account cannot submit a report.")
			return
		}
		if err := validateCSRFRequest(request, maximumReportFormBytes); err != nil {
			serveError(response, request, http.StatusForbidden, "Request verification failed", "Reload the page and try again.")
			return
		}
		targetType, targetID, reason, parseErr := parseReportCreateForm(request)
		if parseErr != nil {
			serveError(response, request, http.StatusBadRequest, "Invalid report", "Reload the page and submit the report again.")
			return
		}
		result, createErr := services.Create(request.Context(), access, targetType, targetID, reason)
		if createErr != nil {
			switch {
			case errors.Is(createErr, moderation.ErrReportInput):
				serveError(response, request, http.StatusUnprocessableEntity, "Invalid report", "Enter a reason without surrounding whitespace.")
			case errors.Is(createErr, moderation.ErrReportDenied):
				serveError(response, request, http.StatusForbidden, "Report denied", "Your current account cannot submit this report.")
			case errors.Is(createErr, moderation.ErrReportDuplicate):
				serveError(response, request, http.StatusConflict, "Already reported", "You already have an active report for this target.")
			case errors.Is(createErr, moderation.ErrReportLimit):
				serveError(response, request, http.StatusTooManyRequests, "Report limit reached", "Resolve existing reports with the moderation team before submitting more.")
			case errors.Is(createErr, pgx.ErrNoRows):
				serveError(response, request, http.StatusNotFound, "Target not found", "The reported target does not exist or is not visible to you.")
			default:
				serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "Reporting is temporarily unavailable.")
			}
			return
		}
		if result.ReportID <= 0 || result.Target != targetType || result.TargetID != targetID || result.Status != "open" {
			serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "Reporting is temporarily unavailable.")
			return
		}
		serveMutationNavigation(response, request, homeURL+"?"+reportSubmittedQuery)
	}
	list := func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request, true)
		if redirect == loginURL || redirect == revalidationURL {
			serveSessionRedirect(response, request, redirect)
			return
		}
		if redirect != "" {
			serveError(response, request, http.StatusForbidden, "Moderation denied", "Your current account cannot view reports.")
			return
		}
		page, parseErr := parseTopicPageQuery(request.URL.RawQuery, store.MaximumReportPage)
		if request.URL.RawPath != "" || parseErr != nil {
			serveError(response, request, http.StatusNotFound, "Page not found", "The requested report page does not exist.")
			return
		}
		loaded, loadErr := services.List(request.Context(), access, page)
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				serveError(response, request, http.StatusNotFound, "Page not found", "The requested report page does not exist.")
			} else {
				serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "The moderation queue is temporarily unavailable.")
			}
			return
		}
		presentation := moderationReportListView{
			Number:  loaded.Number,
			Total:   loaded.Total,
			Reports: make([]moderationReportListItem, 0, len(loaded.Reports)),
		}
		for _, report := range loaded.Reports {
			detailURL, buildErr := builder.Path("moderation", "reports", strconv.FormatInt(report.ID, 10))
			if buildErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "The moderation queue is temporarily unavailable.")
				return
			}
			presentation.Reports = append(presentation.Reports, moderationReportListItem{
				DetailURL:   detailURL,
				TargetType:  report.TargetType,
				TargetLabel: report.TargetLabel,
				Reporter:    report.Reporter,
				Reason:      report.Reason,
				Status:      strings.ReplaceAll(report.Status, "_", " "),
				Assignee:    report.Assignee,
				Created:     report.CreatedAt.UTC().Format("Jan 2, 2006 15:04 MST"),
			})
		}
		var buildErr error
		if page > 1 {
			if page == 2 {
				presentation.PreviousURL, buildErr = builder.Path("moderation", "reports")
			} else {
				presentation.PreviousURL, buildErr = builder.PathWithQuery(
					[]string{"moderation", "reports"},
					url.Values{"page": {strconv.FormatInt(int64(page-1), 10)}},
				)
			}
		}
		if buildErr == nil && int64(page) < loaded.TotalPages {
			presentation.NextURL, buildErr = builder.PathWithQuery(
				[]string{"moderation", "reports"},
				url.Values{"page": {strconv.FormatInt(int64(page+1), 10)}},
			)
		}
		if buildErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "The moderation queue is temporarily unavailable.")
			return
		}
		if renderErr := renderResponse(
			response,
			request,
			http.StatusOK,
			moderationReportListPage(baseView, presentation),
			moderationReportListContent(baseView, presentation),
		); renderErr != nil {
			panic(renderErr)
		}
	}
	detail := func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request, true)
		if redirect == loginURL || redirect == revalidationURL {
			serveSessionRedirect(response, request, redirect)
			return
		}
		if redirect != "" {
			serveError(response, request, http.StatusForbidden, "Moderation denied", "Your current account cannot view reports.")
			return
		}
		reportID, parseErr := parseCanonicalPositiveID(chi.URLParam(request, "reportID"))
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || parseErr != nil {
			serveError(response, request, http.StatusNotFound, "Report not found", "The report does not exist.")
			return
		}
		loaded, loadErr := services.Load(request.Context(), access, reportID)
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				serveError(response, request, http.StatusNotFound, "Report not found", "The report does not exist.")
			} else {
				serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "The report is temporarily unavailable.")
			}
			return
		}
		targetURL := ""
		var buildErr error
		switch loaded.TargetType {
		case "topic":
			targetURL, buildErr = builder.Path("topics", strconv.FormatInt(loaded.TargetID, 10))
		case "post":
			query := make(url.Values)
			if loaded.TargetPage > 1 {
				query.Set("page", strconv.FormatInt(loaded.TargetPage, 10))
			}
			targetURL, buildErr = builder.PathWithQueryAndFragment(
				[]string{"topics", strconv.FormatInt(loaded.TargetTopicID, 10)},
				query,
				"post-"+strconv.FormatInt(loaded.TargetID, 10),
			)
		case "user":
			targetURL, buildErr = builder.Path("moderation", "users", strconv.FormatInt(loaded.TargetID, 10))
		}
		if buildErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "The report is temporarily unavailable.")
			return
		}
		reportIDText := strconv.FormatInt(reportID, 10)
		view, viewErr := newPageView(builder, "Report #"+reportIDText, "moderation", "reports", reportIDText)
		if viewErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "The report is temporarily unavailable.")
			return
		}
		presentation := moderationReportDetailView{
			ID:          reportIDText,
			TargetURL:   targetURL,
			TargetType:  loaded.TargetType,
			TargetLabel: loaded.TargetLabel,
			Reporter:    loaded.Reporter,
			Reason:      loaded.Reason,
			Status:      strings.ReplaceAll(loaded.Status, "_", " "),
			Assignee:    loaded.Assignee,
			Resolution:  loaded.Resolution,
			Resolver:    loaded.Resolver,
			Created:     loaded.CreatedAt.UTC().Format("Jan 2, 2006 15:04 MST"),
			Updated:     loaded.UpdatedAt.UTC().Format("Jan 2, 2006 15:04 MST"),
			CSRFToken:   csrfTokenFromContext(request.Context()),
			Notes:       make([]moderationReportNoteView, 0, len(loaded.Notes)),
		}
		for _, note := range loaded.Notes {
			presentation.Notes = append(presentation.Notes, moderationReportNoteView{
				Author:  note.Author,
				Body:    note.Body,
				Created: note.CreatedAt.UTC().Format("Jan 2, 2006 15:04 MST"),
			})
		}
		if len(presentation.CSRFToken) == sessionCookieEncodedBytes {
			if loaded.Status == "open" {
				presentation.ClaimAction, _ = builder.Path("moderation", "reports", presentation.ID, "claim")
			} else if loaded.Status == "in_review" && (loaded.AssignedTo == access.UserID || access.Role == auth.RoleAdministrator) {
				presentation.NoteAction, _ = builder.Path("moderation", "reports", presentation.ID, "notes")
				presentation.ResolveAction, _ = builder.Path("moderation", "reports", presentation.ID, "resolve")
				presentation.DismissAction, _ = builder.Path("moderation", "reports", presentation.ID, "dismiss")
			}
		}
		if renderErr := renderResponse(
			response,
			request,
			http.StatusOK,
			moderationReportDetailPage(view, presentation),
			moderationReportDetailContent(view, presentation),
		); renderErr != nil {
			panic(renderErr)
		}
	}
	mutate := func(action moderation.ReportAction) http.HandlerFunc {
		return func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set("Cache-Control", "no-store")
			access, redirect := authorized(request, true)
			if redirect == loginURL || redirect == revalidationURL {
				serveSessionRedirect(response, request, redirect)
				return
			}
			if redirect != "" {
				serveError(response, request, http.StatusForbidden, "Moderation denied", "Your current account cannot process reports.")
				return
			}
			reportID, parseErr := parseCanonicalPositiveID(chi.URLParam(request, "reportID"))
			if request.URL.RawPath != "" || request.URL.RawQuery != "" || parseErr != nil {
				serveError(response, request, http.StatusNotFound, "Report not found", "The report does not exist.")
				return
			}
			if csrfErr := validateCSRFRequest(request, maximumReportFormBytes); csrfErr != nil {
				serveError(response, request, http.StatusForbidden, "Request verification failed", "Reload the report and try again.")
				return
			}
			text, formErr := parseReportActionForm(request, action)
			if formErr != nil {
				serveError(response, request, http.StatusBadRequest, "Invalid report action", "Reload the report and submit the action again.")
				return
			}
			requestID, requestErr := moderationRequestUUID(request.Context())
			if requestErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "Report processing is temporarily unavailable.")
				return
			}
			result, processErr := services.Process(request.Context(), access, reportID, action, text, requestID)
			if processErr != nil {
				switch {
				case errors.Is(processErr, moderation.ErrReportInput):
					serveError(response, request, http.StatusUnprocessableEntity, "Invalid report action", "Enter text without surrounding whitespace.")
				case errors.Is(processErr, moderation.ErrReportDenied):
					serveError(response, request, http.StatusForbidden, "Moderation denied", "Your current account cannot process this report.")
				case errors.Is(processErr, moderation.ErrReportConflict):
					serveError(response, request, http.StatusConflict, "Report state changed", "Reload the report before trying another action.")
				case errors.Is(processErr, pgx.ErrNoRows):
					serveError(response, request, http.StatusNotFound, "Report not found", "The report does not exist.")
				default:
					serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "Report processing is temporarily unavailable.")
				}
				return
			}
			if !validReportActionResult(result, reportID, action) {
				serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "Report processing is temporarily unavailable.")
				return
			}
			location, buildErr := builder.Path("moderation", "reports", strconv.FormatInt(reportID, 10))
			if buildErr != nil {
				serveError(response, request, http.StatusServiceUnavailable, "Reports unavailable", "Report processing is temporarily unavailable.")
				return
			}
			serveMutationNavigation(response, request, location)
		}
	}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			contextWithReports := context.WithValue(request.Context(), reportFormsContextKey{}, true)
			next.ServeHTTP(response, request.WithContext(contextWithReports))
		})
	})
	router.Use(captureRoutePattern)
	router.Post("/reports", create)
	router.Get("/moderation/reports", list)
	router.Get("/moderation/reports/{reportID}", detail)
	router.Post("/moderation/reports/{reportID}/claim", mutate(moderation.ClaimReport))
	router.Post("/moderation/reports/{reportID}/notes", mutate(moderation.NoteReport))
	router.Post("/moderation/reports/{reportID}/resolve", mutate(moderation.ResolveReport))
	router.Post("/moderation/reports/{reportID}/dismiss", mutate(moderation.DismissReport))
	router.Post("/moderation/actions", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request, true)
		if redirect == loginURL || redirect == revalidationURL {
			serveSessionRedirect(response, request, redirect)
			return
		}
		if redirect != "" {
			serveError(response, request, http.StatusForbidden, "Moderation denied", "Your current account cannot perform this action.")
			return
		}
		if csrfErr := validateCSRFRequest(request, maximumReportFormBytes); csrfErr != nil {
			serveError(response, request, http.StatusForbidden, "Request verification failed", "Reload the page and try again.")
			return
		}
		input, formErr := parseExtendedActionForm(request)
		if formErr != nil {
			serveError(response, request, http.StatusBadRequest, "Invalid moderation action", "Reload the page and submit the action again.")
			return
		}
		requestID, requestErr := moderationRequestUUID(request.Context())
		if requestErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, "Moderation unavailable", "Moderation is temporarily unavailable.")
			return
		}
		result, actionErr := services.Extended(request.Context(), access, input, requestID)
		if actionErr != nil {
			switch {
			case errors.Is(actionErr, moderation.ErrUserModerationInput):
				serveError(response, request, http.StatusUnprocessableEntity, "Invalid moderation action", "Enter canonical action input.")
			case errors.Is(actionErr, moderation.ErrUserModerationDenied):
				serveError(response, request, http.StatusForbidden, "Moderation denied", "Your current account cannot perform this action.")
			case errors.Is(actionErr, moderation.ErrUserModerationConflict), errors.Is(actionErr, moderation.ErrTopicModerationConflict):
				serveError(response, request, http.StatusConflict, "Target state changed", "Reload the target before trying another moderation action.")
			case errors.Is(actionErr, pgx.ErrNoRows):
				serveError(response, request, http.StatusNotFound, "Target not found", "The moderation target does not exist.")
			default:
				serveError(response, request, http.StatusServiceUnavailable, "Moderation unavailable", "Moderation is temporarily unavailable.")
			}
			return
		}
		contentAction := input.Action == moderation.PinTopic || input.Action == moderation.UnpinTopic ||
			input.Action == moderation.MoveTopic || input.Action == moderation.HidePost ||
			input.Action == moderation.RestorePost || input.Action == moderation.RedactPost
		userAction := input.Action == moderation.WarnUser || input.Action == moderation.MuteUser
		invalidResult := result.TargetID != input.TargetID || result.Action != input.Action ||
			result.AuditID <= 0 || contentAction && result.TopicID <= 0 || userAction && result.TopicID != 0
		if invalidResult {
			serveError(response, request, http.StatusServiceUnavailable, "Moderation unavailable", "Moderation is temporarily unavailable.")
			return
		}
		location := ""
		var buildErr error
		switch input.Action {
		case moderation.PinTopic, moderation.UnpinTopic, moderation.MoveTopic:
			location, buildErr = builder.Path("topics", strconv.FormatInt(result.TopicID, 10))
		case moderation.HidePost, moderation.RestorePost, moderation.RedactPost:
			location, buildErr = builder.PathWithQueryAndFragment(
				[]string{"topics", strconv.FormatInt(result.TopicID, 10)},
				nil,
				"post-"+strconv.FormatInt(result.TargetID, 10),
			)
		case moderation.WarnUser, moderation.MuteUser:
			location, buildErr = builder.Path("moderation", "users", strconv.FormatInt(result.TargetID, 10))
		}
		if buildErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, "Moderation unavailable", "Moderation is temporarily unavailable.")
			return
		}
		serveMutationNavigation(response, request, location)
	})
	return recordRoutePattern(router), nil
}

func validReportActionResult(result moderation.ReportActionResult, reportID int64, action moderation.ReportAction) bool {
	if result.ReportID != reportID || result.AuditID <= 0 {
		return false
	}
	switch action {
	case moderation.ClaimReport:
		return result.Status == "in_review" && result.NoteID == 0
	case moderation.NoteReport:
		return result.Status == "in_review" && result.NoteID > 0
	case moderation.ResolveReport:
		return result.Status == "resolved" && result.NoteID == 0
	case moderation.DismissReport:
		return result.Status == "dismissed" && result.NoteID == 0
	default:
		return false
	}
}

func parseReportCreateForm(request *http.Request) (moderation.ReportTargetType, int64, string, error) {
	if err := request.ParseForm(); err != nil {
		return "", 0, "", err
	}
	allowed := map[string]bool{"_csrf": true, "target_type": true, "target_id": true, "reason": true}
	for key, values := range request.PostForm {
		if !allowed[key] || len(values) != 1 {
			return "", 0, "", fmt.Errorf("invalid report form")
		}
	}
	for _, key := range []string{"target_type", "target_id", "reason"} {
		if len(request.PostForm[key]) != 1 {
			return "", 0, "", fmt.Errorf("invalid report form")
		}
	}
	targetType := moderation.ReportTargetType(request.PostForm.Get("target_type"))
	targetID, err := parseCanonicalPositiveID(request.PostForm.Get("target_id"))
	if err != nil {
		return "", 0, "", err
	}
	return targetType, targetID, request.PostForm.Get("reason"), nil
}

func parseReportActionForm(request *http.Request, action moderation.ReportAction) (string, error) {
	if err := request.ParseForm(); err != nil {
		return "", err
	}
	field := ""
	if action == moderation.NoteReport {
		field = "note"
	} else if action == moderation.ResolveReport || action == moderation.DismissReport {
		field = "resolution"
	}
	allowed := map[string]bool{"_csrf": true}
	if field != "" {
		allowed[field] = true
	}
	for key, values := range request.PostForm {
		if !allowed[key] || len(values) != 1 {
			return "", fmt.Errorf("invalid report action form")
		}
	}
	if field == "" {
		return "", nil
	}
	if len(request.PostForm[field]) != 1 {
		return "", fmt.Errorf("invalid report action form")
	}
	return request.PostForm.Get(field), nil
}

func parseExtendedActionForm(request *http.Request) (moderation.ExtendedActionInput, error) {
	if err := request.ParseForm(); err != nil {
		return moderation.ExtendedActionInput{}, err
	}
	allowed := map[string]bool{
		"_csrf": true, "action": true, "target_id": true, "reason": true,
		"destination_area_slug": true, "mute_duration": true,
	}
	for key, values := range request.PostForm {
		if !allowed[key] || len(values) != 1 {
			return moderation.ExtendedActionInput{}, fmt.Errorf("invalid extended moderation form")
		}
	}
	for _, key := range []string{"action", "target_id", "reason"} {
		if len(request.PostForm[key]) != 1 {
			return moderation.ExtendedActionInput{}, fmt.Errorf("invalid extended moderation form")
		}
	}
	targetID, err := parseCanonicalPositiveID(request.PostForm.Get("target_id"))
	if err != nil {
		return moderation.ExtendedActionInput{}, err
	}
	input := moderation.ExtendedActionInput{
		Action:   moderation.ExtendedAction(request.PostForm.Get("action")),
		TargetID: targetID,
		Reason:   request.PostForm.Get("reason"),
	}
	if values, ok := request.PostForm["destination_area_slug"]; ok {
		input.DestinationAreaSlug = values[0]
	}
	if values, ok := request.PostForm["mute_duration"]; ok {
		durations := map[string]time.Duration{
			"1h": time.Hour, "24h": 24 * time.Hour,
			"168h": 7 * 24 * time.Hour, "720h": 30 * 24 * time.Hour,
		}
		input.MuteDuration, ok = durations[values[0]]
		if !ok {
			return moderation.ExtendedActionInput{}, fmt.Errorf("invalid mute duration")
		}
	}
	return input, nil
}
