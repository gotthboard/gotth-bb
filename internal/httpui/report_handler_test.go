package httpui

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/moderation"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestReportHandlerCreatesReportWithExactAuthority(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember}
	services := reportTestServices()
	services.Create = func(ctx context.Context, gotActor auth.AccessContext, target moderation.ReportTargetType, targetID int64, reason string) (moderation.ReportResult, error) {
		if !reportFormsEnabled(ctx) || !reflect.DeepEqual(gotActor, actor) || target != moderation.ReportPost || targetID != 91 || reason != "Needs review" {
			t.Fatalf("create report call = (%t, %+v, %q, %d, %q)", reportFormsEnabled(ctx), gotActor, target, targetID, reason)
		}
		return moderation.ReportResult{ReportID: 7, Target: target, TargetID: targetID, Status: "open", CreatedAt: reportHandlerTime(10)}, nil
	}
	handler := newReportTestHandler(t, services, false)
	for _, htmx := range []bool{false, true} {
		request := moderationTestRequest("/reports", url.Values{
			"_csrf": {validCSRFTokenForTest(0x51)}, "target_type": {"post"}, "target_id": {"91"}, "reason": {"Needs review"},
		}, auth.SessionAuthentication{SessionID: 3, Access: actor})
		if htmx {
			request.Header.Set("HX-Request", "true")
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Header().Get("Cache-Control") != "no-store" || request.Pattern != "POST /reports" {
			t.Fatalf("create response = (status %d, headers %v, pattern %q, body %q)", response.Code, response.Header(), request.Pattern, response.Body.String())
		}
		if htmx {
			want := `{"path":"/bb/?report=submitted","target":"#main-content","swap":"outerHTML"}`
			if response.Code != http.StatusNoContent || response.Header().Get("HX-Location") != want || response.Header().Get("Location") != "" {
				t.Fatalf("HTMX create response = (status %d, headers %v)", response.Code, response.Header())
			}
		} else if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/?"+reportSubmittedQuery {
			t.Fatalf("ordinary create response = (status %d, headers %v)", response.Code, response.Header())
		}
	}
}

func TestReportHandlerListsAndLoadsReports(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}
	services := reportTestServices()
	services.List = func(ctx context.Context, gotActor auth.AccessContext, page int32) (store.ModerationReportPage, error) {
		if !reportFormsEnabled(ctx) || !reflect.DeepEqual(gotActor, actor) || page != 2 {
			t.Fatalf("list report call = (%t, %+v, %d)", reportFormsEnabled(ctx), gotActor, page)
		}
		reports := make([]store.ModerationReportSummary, store.ReportPageSize)
		for index := range reports {
			reports[index] = store.ModerationReportSummary{
				ID: int64(7 + index), TargetID: int64(91 + index), Reason: "Needs <review>", Status: "in_review", Reporter: "Reporter",
				Assignee: "Moderator", TargetType: "post", TargetLabel: "Visible topic", CreatedAt: reportHandlerTime(10),
			}
		}
		return store.ModerationReportPage{
			Number: 2, Total: 51, TotalPages: 3,
			Reports: reports,
		}, nil
	}
	services.Load = func(ctx context.Context, gotActor auth.AccessContext, reportID int64) (store.ModerationReportDetail, error) {
		if !reportFormsEnabled(ctx) || !reflect.DeepEqual(gotActor, actor) || reportID != 7 {
			t.Fatalf("load report call = (%t, %+v, %d)", reportFormsEnabled(ctx), gotActor, reportID)
		}
		return store.ModerationReportDetail{
			ID: 7, ReporterID: 5, TargetID: 91, TargetTopicID: 81, TargetPage: 3,
			Reporter: "Reporter", Reason: "Needs <review>", Status: "in_review",
			Assignee: "Moderator", AssignedTo: actor.UserID, TargetType: "post", TargetLabel: "Visible topic",
			CreatedAt: reportHandlerTime(10), UpdatedAt: reportHandlerTime(11),
			Notes: []store.ModerationReportNote{{ID: 2, AuthorID: actor.UserID, Author: "Moderator", Body: "Private <note>", CreatedAt: reportHandlerTime(11)}},
		}, nil
	}
	handler := newReportTestHandler(t, services, false)

	listRequest := reportHandlerGET("/moderation/reports?page=2", actor)
	listResponse := httptest.NewRecorder()
	handler.ServeHTTP(listResponse, listRequest)
	listBody := listResponse.Body.String()
	if listResponse.Code != http.StatusOK || listRequest.Pattern != "GET /moderation/reports" ||
		!strings.Contains(listBody, "Needs &lt;review&gt;") || !strings.Contains(listBody, `href="/bb/moderation/reports/7"`) ||
		!strings.Contains(listBody, `href="/bb/moderation/reports"`) || !strings.Contains(listBody, `href="/bb/moderation/reports?page=3"`) ||
		!strings.Contains(listBody, ">Reports</a>") {
		t.Fatalf("list response = (status %d, pattern %q, body %q)", listResponse.Code, listRequest.Pattern, listBody)
	}

	detailRequest := reportHandlerGET("/moderation/reports/7", actor)
	detailResponse := httptest.NewRecorder()
	handler.ServeHTTP(detailResponse, detailRequest)
	detailBody := detailResponse.Body.String()
	for _, want := range []string{
		"Report #7", "Needs &lt;review&gt;", "Private &lt;note&gt;", `href="/bb/topics/81?page=3#post-91"`,
		`action="/bb/moderation/reports/7/notes"`, `action="/bb/moderation/reports/7/resolve"`,
	} {
		if !strings.Contains(detailBody, want) {
			t.Fatalf("detail response missing %q: %q", want, detailBody)
		}
	}
	if detailResponse.Code != http.StatusOK || detailRequest.Pattern != "GET /moderation/reports/{reportID}" || !strings.Contains(detailResponse.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("detail response = (status %d, headers %v, pattern %q)", detailResponse.Code, detailResponse.Header(), detailRequest.Pattern)
	}
}

func TestReportHandlerRejectsMalformedReadResults(t *testing.T) {
	t.Parallel()
	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}
	for _, test := range []struct {
		name   string
		target string
		list   *store.ModerationReportPage
		detail *store.ModerationReportDetail
	}{
		{name: "wrong list page", target: "/moderation/reports", list: &store.ModerationReportPage{Number: 2}},
		{name: "incomplete list", target: "/moderation/reports", list: &store.ModerationReportPage{Number: 1, Total: 2, TotalPages: 1}},
		{name: "wrong detail identity", target: "/moderation/reports/7", detail: &store.ModerationReportDetail{ID: 8}},
		{name: "unknown detail target", target: "/moderation/reports/7", detail: &store.ModerationReportDetail{
			ID: 7, ReporterID: 5, TargetID: 9, Reporter: "Reporter", Reason: "Reason", Status: "open",
			TargetType: "unknown", TargetLabel: "Target", CreatedAt: reportHandlerTime(9), UpdatedAt: reportHandlerTime(10),
		}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			services := reportTestServices()
			if test.list != nil {
				services.List = func(context.Context, auth.AccessContext, int32) (store.ModerationReportPage, error) {
					return *test.list, nil
				}
			} else {
				services.Load = func(context.Context, auth.AccessContext, int64) (store.ModerationReportDetail, error) {
					return *test.detail, nil
				}
			}
			handler := newReportTestHandler(t, services, false)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, reportHandlerGET(test.target, actor))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("malformed read response = (status %d, body %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestReportHandlerProcessesEveryReportTransition(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}
	for _, test := range []struct {
		suffix string
		action moderation.ReportAction
		field  string
		text   string
		htmx   bool
	}{
		{suffix: "claim", action: moderation.ClaimReport},
		{suffix: "notes", action: moderation.NoteReport, field: "note", text: "Checked context", htmx: true},
		{suffix: "resolve", action: moderation.ResolveReport, field: "resolution", text: "Handled"},
		{suffix: "dismiss", action: moderation.DismissReport, field: "resolution", text: "No violation"},
	} {
		test := test
		t.Run(test.suffix, func(t *testing.T) {
			t.Parallel()
			services := reportTestServices()
			services.Process = func(ctx context.Context, gotActor auth.AccessContext, reportID int64, action moderation.ReportAction, text string, requestID pgtype.UUID) (moderation.ReportActionResult, error) {
				if !reportFormsEnabled(ctx) || !reflect.DeepEqual(gotActor, actor) || reportID != 7 || action != test.action || text != test.text || !requestID.Valid || requestID.Bytes[0] != 0x51 {
					t.Fatalf("process call = (%t, %+v, %d, %q, %q, %+v)", reportFormsEnabled(ctx), gotActor, reportID, action, text, requestID)
				}
				result := moderation.ReportActionResult{ReportID: reportID, Status: "in_review", AuditID: 17}
				switch test.action {
				case moderation.NoteReport:
					result.NoteID = 23
				case moderation.ResolveReport:
					result.Status = "resolved"
				case moderation.DismissReport:
					result.Status = "dismissed"
				}
				return result, nil
			}
			handler := newReportTestHandler(t, services, true)
			form := url.Values{"_csrf": {validCSRFTokenForTest(0x51)}}
			if test.field != "" {
				form.Set(test.field, test.text)
			}
			request := moderationTestRequest("/moderation/reports/7/"+test.suffix, form, auth.SessionAuthentication{SessionID: 3, Access: actor})
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if test.htmx {
				want := `{"path":"/bb/moderation/reports/7","target":"#main-content","swap":"outerHTML"}`
				if response.Code != http.StatusNoContent || response.Header().Get("HX-Location") != want || response.Header().Get("Location") != "" {
					t.Fatalf("HTMX process response = (status %d, headers %v)", response.Code, response.Header())
				}
			} else if response.Code != http.StatusSeeOther || response.Header().Get("Location") != "/bb/moderation/reports/7" {
				t.Fatalf("process response = (status %d, headers %v, pattern %q, body %q)", response.Code, response.Header(), request.Pattern, response.Body.String())
			}
		})
	}
}

func TestReportHandlerAppliesEveryExtendedAction(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleAdministrator}
	for _, test := range []struct {
		action   moderation.ExtendedAction
		extraKey string
		extra    string
		wantURL  string
		htmx     bool
	}{
		{action: moderation.PinTopic, wantURL: "/bb/topics/81", htmx: true},
		{action: moderation.UnpinTopic, wantURL: "/bb/topics/81"},
		{action: moderation.MoveTopic, extraKey: "destination_area_slug", extra: "archive", wantURL: "/bb/topics/81"},
		{action: moderation.HidePost, wantURL: "/bb/topics/81?page=3#post-91"},
		{action: moderation.RestorePost, wantURL: "/bb/topics/81?page=3#post-91"},
		{action: moderation.RedactPost, wantURL: "/bb/topics/81?page=3#post-91"},
		{action: moderation.WarnUser, wantURL: "/bb/moderation/users/91"},
		{action: moderation.MuteUser, extraKey: "mute_duration", extra: "24h", wantURL: "/bb/moderation/users/91"},
	} {
		test := test
		t.Run(string(test.action), func(t *testing.T) {
			t.Parallel()
			targetID := int64(91)
			if test.action == moderation.PinTopic || test.action == moderation.UnpinTopic || test.action == moderation.MoveTopic {
				targetID = 81
			}
			services := reportTestServices()
			services.Extended = func(ctx context.Context, gotActor auth.AccessContext, input moderation.ExtendedActionInput, requestID pgtype.UUID) (moderation.ExtendedActionResult, error) {
				if !reportFormsEnabled(ctx) || !reflect.DeepEqual(gotActor, actor) || input.Action != test.action || input.TargetID != targetID || input.Reason != "Clear reason" || !requestID.Valid {
					t.Fatalf("extended call = (%t, %+v, %+v, %+v)", reportFormsEnabled(ctx), gotActor, input, requestID)
				}
				if test.action == moderation.MoveTopic && input.DestinationAreaSlug != "archive" {
					t.Fatalf("move destination = %q", input.DestinationAreaSlug)
				}
				if test.action == moderation.MuteUser && input.MuteDuration != 24*time.Hour {
					t.Fatalf("mute duration = %s", input.MuteDuration)
				}
				result := moderation.ExtendedActionResult{Action: input.Action, TargetID: input.TargetID, AuditID: 19}
				switch input.Action {
				case moderation.PinTopic, moderation.UnpinTopic, moderation.MoveTopic:
					result.TopicID = 81
					result.TargetID = 81
				case moderation.HidePost, moderation.RestorePost, moderation.RedactPost:
					result.TopicID, result.TargetPage = 81, 3
				case moderation.WarnUser:
					result.WarningID = 23
				case moderation.MuteUser:
					mutedUntil := reportHandlerTime(12)
					result.MutedUntil = &mutedUntil
				}
				return result, nil
			}
			handler := newReportTestHandler(t, services, true)
			form := url.Values{
				"_csrf": {validCSRFTokenForTest(0x51)}, "action": {string(test.action)},
				"target_id": {strconv.FormatInt(targetID, 10)}, "reason": {"Clear reason"},
			}
			if test.extraKey != "" {
				form.Set(test.extraKey, test.extra)
			}
			request := moderationTestRequest("/moderation/actions", form, auth.SessionAuthentication{SessionID: 3, Access: actor})
			if test.htmx {
				request.Header.Set("HX-Request", "true")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if test.htmx {
				want := `{"path":"` + test.wantURL + `","target":"#main-content","swap":"outerHTML"}`
				if response.Code != http.StatusNoContent || response.Header().Get("HX-Location") != want || response.Header().Get("Location") != "" {
					t.Fatalf("HTMX extended response = (status %d, headers %v)", response.Code, response.Header())
				}
			} else if response.Code != http.StatusSeeOther || response.Header().Get("Location") != test.wantURL {
				t.Fatalf("extended response = (status %d, headers %v, pattern %q, body %q)", response.Code, response.Header(), request.Pattern, response.Body.String())
			}
		})
	}
}

func TestValidExtendedActionResultClosesEveryVariant(t *testing.T) {
	t.Parallel()
	mutedUntil := reportHandlerTime(12)
	valid := []struct {
		input  moderation.ExtendedActionInput
		result moderation.ExtendedActionResult
	}{
		{moderation.ExtendedActionInput{Action: moderation.PinTopic, TargetID: 81}, moderation.ExtendedActionResult{Action: moderation.PinTopic, TargetID: 81, TopicID: 81, AuditID: 1}},
		{moderation.ExtendedActionInput{Action: moderation.HidePost, TargetID: 91}, moderation.ExtendedActionResult{Action: moderation.HidePost, TargetID: 91, TopicID: 81, TargetPage: 3, AuditID: 1}},
		{moderation.ExtendedActionInput{Action: moderation.WarnUser, TargetID: 91}, moderation.ExtendedActionResult{Action: moderation.WarnUser, TargetID: 91, WarningID: 2, AuditID: 1}},
		{moderation.ExtendedActionInput{Action: moderation.MuteUser, TargetID: 91}, moderation.ExtendedActionResult{Action: moderation.MuteUser, TargetID: 91, MutedUntil: &mutedUntil, AuditID: 1}},
	}
	for _, test := range valid {
		if !validExtendedActionResult(test.result, test.input) {
			t.Fatalf("validExtendedActionResult(%+v, %+v) rejected valid result", test.result, test.input)
		}
	}
	invalid := []struct {
		input  moderation.ExtendedActionInput
		result moderation.ExtendedActionResult
	}{
		{moderation.ExtendedActionInput{Action: moderation.PinTopic, TargetID: 81}, moderation.ExtendedActionResult{Action: moderation.PinTopic, TargetID: 81, TopicID: 82, AuditID: 1}},
		{moderation.ExtendedActionInput{Action: moderation.HidePost, TargetID: 91}, moderation.ExtendedActionResult{Action: moderation.HidePost, TargetID: 91, TopicID: 81, AuditID: 1}},
		{moderation.ExtendedActionInput{Action: moderation.WarnUser, TargetID: 91}, moderation.ExtendedActionResult{Action: moderation.WarnUser, TargetID: 91, AuditID: 1}},
		{moderation.ExtendedActionInput{Action: moderation.MuteUser, TargetID: 91}, moderation.ExtendedActionResult{Action: moderation.MuteUser, TargetID: 91, AuditID: 1}},
	}
	for _, test := range invalid {
		if validExtendedActionResult(test.result, test.input) {
			t.Fatalf("validExtendedActionResult(%+v, %+v) accepted malformed result", test.result, test.input)
		}
	}
}

func TestReportHandlerFailsClosedBeforeCallingServices(t *testing.T) {
	t.Parallel()

	services := reportTestServices()
	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}
	for _, test := range []struct {
		name           string
		method, target string
		form           url.Values
		authentication auth.SessionAuthentication
		wantStatus     int
		wantLocation   string
	}{
		{name: "anonymous create", method: http.MethodPost, target: "/reports", form: validReportCreateForm(), wantStatus: http.StatusSeeOther, wantLocation: "/bb/login"},
		{name: "member queue", method: http.MethodGet, target: "/moderation/reports", authentication: auth.SessionAuthentication{SessionID: 3, Access: auth.AccessContext{Authenticated: true, UserID: 8, Role: auth.RoleMember}}, wantStatus: http.StatusForbidden},
		{name: "bad page", method: http.MethodGet, target: "/moderation/reports?page=0", authentication: auth.SessionAuthentication{SessionID: 3, Access: actor}, wantStatus: http.StatusNotFound},
		{name: "bad detail ID", method: http.MethodGet, target: "/moderation/reports/07", authentication: auth.SessionAuthentication{SessionID: 3, Access: actor}, wantStatus: http.StatusNotFound},
		{name: "missing CSRF", method: http.MethodPost, target: "/moderation/reports/7/claim", form: url.Values{}, authentication: auth.SessionAuthentication{SessionID: 3, Access: actor}, wantStatus: http.StatusForbidden},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := newReportTestHandler(t, services, true)
			var request *http.Request
			if test.method == http.MethodPost {
				request = moderationTestRequest(test.target, test.form, test.authentication)
			} else {
				request = reportHandlerGET(test.target, test.authentication.Access)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("Location") != test.wantLocation {
				t.Fatalf("response = (status %d, headers %v, body %q)", response.Code, response.Header(), response.Body.String())
			}
		})
	}
}

func TestReportHandlerMapsCreateFailuresWithoutLeakingCauses(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleMember}
	secret := errors.New("secret database failure")
	for _, test := range []struct {
		name       string
		result     moderation.ReportResult
		cause      error
		wantStatus int
	}{
		{name: "input", cause: moderation.ErrReportInput, wantStatus: http.StatusUnprocessableEntity},
		{name: "denied", cause: moderation.ErrReportDenied, wantStatus: http.StatusForbidden},
		{name: "duplicate", cause: moderation.ErrReportDuplicate, wantStatus: http.StatusConflict},
		{name: "limit", cause: moderation.ErrReportLimit, wantStatus: http.StatusTooManyRequests},
		{name: "missing", cause: pgx.ErrNoRows, wantStatus: http.StatusNotFound},
		{name: "store", cause: secret, wantStatus: http.StatusServiceUnavailable},
		{name: "malformed success", result: moderation.ReportResult{ReportID: -1}, wantStatus: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			services := reportTestServices()
			services.Create = func(context.Context, auth.AccessContext, moderation.ReportTargetType, int64, string) (moderation.ReportResult, error) {
				return test.result, test.cause
			}
			handler := newReportTestHandler(t, services, false)
			request := moderationTestRequest("/reports", validReportCreateForm(), auth.SessionAuthentication{SessionID: 3, Access: actor})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || strings.Contains(response.Body.String(), secret.Error()) {
				t.Fatalf("response = (status %d, body %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestReportHandlerMapsReadFailures(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}
	for _, test := range []struct {
		name       string
		target     string
		cause      error
		wantStatus int
		list       bool
	}{
		{name: "missing list page", target: "/moderation/reports?page=2", cause: pgx.ErrNoRows, wantStatus: http.StatusNotFound, list: true},
		{name: "list failure", target: "/moderation/reports", cause: context.Canceled, wantStatus: http.StatusServiceUnavailable, list: true},
		{name: "missing detail", target: "/moderation/reports/7", cause: pgx.ErrNoRows, wantStatus: http.StatusNotFound},
		{name: "detail failure", target: "/moderation/reports/7", cause: context.Canceled, wantStatus: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			services := reportTestServices()
			if test.list {
				services.List = func(context.Context, auth.AccessContext, int32) (store.ModerationReportPage, error) {
					return store.ModerationReportPage{}, test.cause
				}
			} else {
				services.Load = func(context.Context, auth.AccessContext, int64) (store.ModerationReportDetail, error) {
					return store.ModerationReportDetail{}, test.cause
				}
			}
			handler := newReportTestHandler(t, services, false)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, reportHandlerGET(test.target, actor))
			if response.Code != test.wantStatus {
				t.Fatalf("response = (status %d, body %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestReportHandlerMapsMutationFailures(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}
	for _, test := range []struct {
		name       string
		cause      error
		result     moderation.ReportActionResult
		wantStatus int
	}{
		{name: "input", cause: moderation.ErrReportInput, wantStatus: http.StatusUnprocessableEntity},
		{name: "denied", cause: moderation.ErrReportDenied, wantStatus: http.StatusForbidden},
		{name: "conflict", cause: moderation.ErrReportConflict, wantStatus: http.StatusConflict},
		{name: "missing", cause: pgx.ErrNoRows, wantStatus: http.StatusNotFound},
		{name: "store", cause: context.Canceled, wantStatus: http.StatusServiceUnavailable},
		{name: "malformed success", result: moderation.ReportActionResult{ReportID: 7}, wantStatus: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			services := reportTestServices()
			services.Process = func(context.Context, auth.AccessContext, int64, moderation.ReportAction, string, pgtype.UUID) (moderation.ReportActionResult, error) {
				return test.result, test.cause
			}
			handler := newReportTestHandler(t, services, true)
			request := moderationTestRequest("/moderation/reports/7/claim", url.Values{
				"_csrf": {validCSRFTokenForTest(0x51)},
			}, auth.SessionAuthentication{SessionID: 3, Access: actor})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("response = (status %d, body %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestReportHandlerMapsExtendedMutationFailures(t *testing.T) {
	t.Parallel()

	actor := auth.AccessContext{Authenticated: true, UserID: 42, Role: auth.RoleModerator}
	for _, test := range []struct {
		name       string
		cause      error
		result     moderation.ExtendedActionResult
		wantStatus int
	}{
		{name: "input", cause: moderation.ErrUserModerationInput, wantStatus: http.StatusUnprocessableEntity},
		{name: "denied", cause: moderation.ErrUserModerationDenied, wantStatus: http.StatusForbidden},
		{name: "user conflict", cause: moderation.ErrUserModerationConflict, wantStatus: http.StatusConflict},
		{name: "topic conflict", cause: moderation.ErrTopicModerationConflict, wantStatus: http.StatusConflict},
		{name: "missing", cause: pgx.ErrNoRows, wantStatus: http.StatusNotFound},
		{name: "store", cause: context.Canceled, wantStatus: http.StatusServiceUnavailable},
		{name: "malformed success", result: moderation.ExtendedActionResult{Action: moderation.PinTopic, TargetID: 91}, wantStatus: http.StatusServiceUnavailable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			services := reportTestServices()
			services.Extended = func(context.Context, auth.AccessContext, moderation.ExtendedActionInput, pgtype.UUID) (moderation.ExtendedActionResult, error) {
				return test.result, test.cause
			}
			handler := newReportTestHandler(t, services, true)
			request := moderationTestRequest("/moderation/actions", url.Values{
				"_csrf": {validCSRFTokenForTest(0x51)}, "action": {"pin_topic"},
				"target_id": {"91"}, "reason": {"Reason"},
			}, auth.SessionAuthentication{SessionID: 3, Access: actor})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("response = (status %d, body %q)", response.Code, response.Body.String())
			}
		})
	}
}

func TestParseReportCreateFormRequiresExactCanonicalFields(t *testing.T) {
	t.Parallel()
	valid := url.Values{"_csrf": {"token"}, "target_type": {"post"}, "target_id": {"41"}, "reason": {"Concern"}}
	request := httptest.NewRequest(http.MethodPost, "/reports", strings.NewReader(valid.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	targetType, targetID, reason, err := parseReportCreateForm(request)
	if err != nil || targetType != moderation.ReportPost || targetID != 41 || reason != "Concern" {
		t.Fatalf("parseReportCreateForm() = (%q, %d, %q, %v)", targetType, targetID, reason, err)
	}
	for _, form := range []url.Values{
		{"_csrf": {"token"}, "target_type": {"post"}, "target_id": {"041"}, "reason": {"Concern"}},
		{"_csrf": {"token"}, "target_type": {"post"}, "target_id": {"41"}},
		{"_csrf": {"token"}, "target_type": {"post"}, "target_id": {"41"}, "reason": {"Concern"}, "extra": {"x"}},
		{"_csrf": {"token"}, "target_type": {"post", "topic"}, "target_id": {"41"}, "reason": {"Concern"}},
	} {
		request := httptest.NewRequest(http.MethodPost, "/reports", strings.NewReader(form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if _, _, _, err := parseReportCreateForm(request); err == nil {
			t.Fatalf("parseReportCreateForm(%v) accepted malformed form", form)
		}
	}
}

func TestParseReportActionFormsAreClosed(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		action moderation.ReportAction
		form   url.Values
		want   string
	}{
		{action: moderation.ClaimReport, form: url.Values{"_csrf": {"token"}}},
		{action: moderation.NoteReport, form: url.Values{"_csrf": {"token"}, "note": {"Note"}}, want: "Note"},
		{action: moderation.ResolveReport, form: url.Values{"_csrf": {"token"}, "resolution": {"Resolved"}}, want: "Resolved"},
	} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.form.Encode()))
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		got, err := parseReportActionForm(request, test.action)
		if err != nil || got != test.want {
			t.Fatalf("parseReportActionForm() = (%q, %v), want %q", got, err, test.want)
		}
	}
}

func TestParseExtendedActionFormClosesDurationsAndExtraFields(t *testing.T) {
	t.Parallel()
	form := url.Values{"_csrf": {"token"}, "action": {"mute_user"}, "target_id": {"41"}, "reason": {"Reason"}, "mute_duration": {"168h"}}
	request := httptest.NewRequest(http.MethodPost, "/moderation/actions", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	got, err := parseExtendedActionForm(request)
	if err != nil || got.Action != moderation.MuteUser || got.TargetID != 41 || got.Reason != "Reason" || got.MuteDuration != 7*24*time.Hour {
		t.Fatalf("parseExtendedActionForm() = (%+v, %v)", got, err)
	}
	form.Set("mute_duration", "2h")
	request = httptest.NewRequest(http.MethodPost, "/moderation/actions", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, err := parseExtendedActionForm(request); err == nil {
		t.Fatal("parseExtendedActionForm accepted unapproved duration")
	}
}

func TestNewReportHandlerRejectsIncompleteServices(t *testing.T) {
	t.Parallel()

	services := reportTestServices()
	for _, clear := range []func(*ReportHTTPServices){
		func(value *ReportHTTPServices) { value.Create = nil },
		func(value *ReportHTTPServices) { value.List = nil },
		func(value *ReportHTTPServices) { value.Load = nil },
		func(value *ReportHTTPServices) { value.Process = nil },
		func(value *ReportHTTPServices) { value.Extended = nil },
	} {
		candidate := services
		clear(&candidate)
		if handler, err := newReportHandler(callbackTestURLBuilder(t), candidate); err == nil || handler != nil {
			t.Fatalf("newReportHandler(incomplete) = (%v, %v), want nil/error", handler, err)
		}
	}
}

func reportTestServices() ReportHTTPServices {
	return ReportHTTPServices{
		Create: func(context.Context, auth.AccessContext, moderation.ReportTargetType, int64, string) (moderation.ReportResult, error) {
			panic("unexpected report create")
		},
		List: func(context.Context, auth.AccessContext, int32) (store.ModerationReportPage, error) {
			panic("unexpected report list")
		},
		Load: func(context.Context, auth.AccessContext, int64) (store.ModerationReportDetail, error) {
			panic("unexpected report load")
		},
		Process: func(context.Context, auth.AccessContext, int64, moderation.ReportAction, string, pgtype.UUID) (moderation.ReportActionResult, error) {
			panic("unexpected report process")
		},
		Extended: func(context.Context, auth.AccessContext, moderation.ExtendedActionInput, pgtype.UUID) (moderation.ExtendedActionResult, error) {
			panic("unexpected extended action")
		},
	}
}

func newReportTestHandler(t *testing.T, services ReportHTTPServices, requestID bool) http.Handler {
	t.Helper()
	handler, err := newReportHandler(callbackTestURLBuilder(t), services)
	if err != nil {
		t.Fatalf("newReportHandler() returned error: %v", err)
	}
	if requestID {
		return withModerationTestRequestID(t, handler)
	}
	return handler
}

func reportHandlerGET(target string, actor auth.AccessContext) *http.Request {
	request := httptest.NewRequest(http.MethodGet, target, nil)
	ctx := context.WithValue(request.Context(), sessionAuthenticationContextKey{}, auth.SessionAuthentication{SessionID: 3, Access: actor})
	ctx = context.WithValue(ctx, csrfTokenContextKey{}, validCSRFTokenForTest(0x51))
	return request.WithContext(ctx)
}

func validReportCreateForm() url.Values {
	return url.Values{
		"_csrf": {validCSRFTokenForTest(0x51)}, "target_type": {"topic"},
		"target_id": {"91"}, "reason": {"Needs review"},
	}
}

func reportHandlerTime(hour int) time.Time {
	return time.Date(2026, time.September, 6, hour, 0, 0, 0, time.UTC)
}
