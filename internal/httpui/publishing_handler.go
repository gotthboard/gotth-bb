package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/gotthboard/gotth-bb/internal/auth"
	"github.com/gotthboard/gotth-bb/internal/forum"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/jackc/pgx/v5"
)

const maximumPublishingFormBytes = 262_144

type TopicPublisher func(context.Context, auth.AccessContext, string, string, string) (forum.PublishResult, error)
type ReplyPublisher func(context.Context, auth.AccessContext, int64, int64, string) (forum.PublishResult, error)

// newPublishingHandler constructs the authenticated topic/reply form boundary.
// CSRF validation always precedes form parsing and mutation. Validation errors
// render the submitted source back through escaped Templ values; successful
// publication redirects only to builder-owned canonical paths.
//
// Complexity: construction is tight Theta(1) time and space. For a bounded
// form body n <= 262,144, delegated publication or rendering work D, and
// response bytes r, request time is O(n+D+r), Omega(1), and auxiliary space is
// O(n+r), Omega(1), without a tighter bound because PostgreSQL, rendering, and
// writer work vary. Each delegated operation runs at most once; no request is
// retried or detached.
func newPublishingHandler(builder URLBuilder, createTopic TopicPublisher, createReply ReplyPublisher) (http.Handler, error) {
	if createTopic == nil {
		return nil, fmt.Errorf("topic publisher is required")
	}
	if createReply == nil {
		return nil, fmt.Errorf("reply publisher is required")
	}
	topicAction, err := builder.Path("topics")
	if err != nil {
		return nil, fmt.Errorf("build topic publishing action: %w", err)
	}
	topicPreviewAction, err := builder.Path("topics", "preview")
	if err != nil {
		return nil, fmt.Errorf("build topic preview action: %w", err)
	}
	loginURL, err := builder.Path("login")
	if err != nil {
		return nil, fmt.Errorf("build publishing login URL: %w", err)
	}
	revalidationURL, err := builder.Path("auth", "revalidate")
	if err != nil {
		return nil, fmt.Errorf("build publishing revalidation URL: %w", err)
	}
	baseView, err := newPageView(builder, "Publish")
	if err != nil {
		return nil, fmt.Errorf("construct publishing view: %w", err)
	}
	baseView.CanonicalURL = ""

	renderForm := func(response http.ResponseWriter, request *http.Request, status int, form publishingFormView) {
		view := baseView
		view.Title = form.Heading
		if renderErr := renderResponse(response, request, status, publishingFormPage(view, form), publishingFormContent(view, form)); renderErr != nil {
			panic(renderErr)
		}
	}
	serveFailure := func(response http.ResponseWriter, status int, message string) {
		http.Error(response, message, status)
	}
	authorized := func(request *http.Request) (auth.AccessContext, string) {
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
			return auth.AccessContext{}, loginURL
		}
		if authentication.RequiresRevalidation {
			return auth.AccessContext{}, revalidationURL
		}
		return authentication.Access, ""
	}
	csrfToken := func(request *http.Request) (string, bool) {
		token := csrfTokenFromContext(request.Context())
		return token, len(token) == sessionCookieEncodedBytes
	}

	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Get("/topics/new", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if _, redirect := authorized(request); redirect != "" {
			serveSessionRedirect(response, request, redirect)
			return
		}
		areaSlug, parseErr := parseNewTopicArea(request.URL.RawQuery)
		if request.URL.RawPath != "" || parseErr != nil {
			serveFailure(response, http.StatusBadRequest, "invalid topic area")
			return
		}
		token, ok := csrfToken(request)
		if !ok {
			serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
			return
		}
		cancelURL, buildErr := builder.Path("areas", areaSlug)
		if buildErr != nil {
			serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
			return
		}
		renderForm(response, request, http.StatusOK, publishingFormView{
			Heading: "New topic", ActionURL: topicAction, PreviewURL: topicPreviewAction, CancelURL: cancelURL,
			CSRFToken: token, AreaSlug: areaSlug,
		})
	})
	router.Post("/topics/preview", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		_, redirect := authorized(request)
		if redirect != "" {
			serveSessionRedirect(response, request, redirect)
			return
		}
		if request.URL.RawPath != "" || request.URL.RawQuery != "" {
			serveFailure(response, http.StatusBadRequest, "invalid preview request")
			return
		}
		if err := validateCSRFRequest(request, maximumPublishingFormBytes); err != nil {
			serveFailure(response, http.StatusForbidden, "request verification failed")
			return
		}
		form, parseErr := parsePublishingForm(request, false)
		if parseErr != nil {
			serveFailure(response, http.StatusBadRequest, "invalid preview form")
			return
		}
		cancelURL, buildErr := builder.Path("areas", form.AreaSlug)
		if buildErr != nil {
			serveFailure(response, http.StatusServiceUnavailable, "preview unavailable")
			return
		}
		form.Heading, form.ActionURL, form.PreviewURL, form.CancelURL = "New topic", topicAction, topicPreviewAction, cancelURL
		form.CSRFToken = csrfTokenFromContext(request.Context())
		rendered, previewErr := forum.RenderTopicDraft(form.AreaSlug, form.Title, form.Markdown)
		if previewErr != nil {
			if invalid, validation := publishingValidation(previewErr); validation {
				applyPublishingValidation(&form, invalid)
				renderForm(response, request, http.StatusUnprocessableEntity, form)
				return
			}
			serveFailure(response, http.StatusServiceUnavailable, "preview unavailable")
			return
		}
		form.PreviewBody, form.ShowPreview = rendered.TrustedHTML(), true
		renderForm(response, request, http.StatusOK, form)
	})
	router.Post("/topics", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request)
		if redirect != "" {
			serveSessionRedirect(response, request, redirect)
			return
		}
		if request.URL.RawPath != "" || request.URL.RawQuery != "" {
			serveFailure(response, http.StatusBadRequest, "invalid publishing request")
			return
		}
		if err := validateCSRFRequest(request, maximumPublishingFormBytes); err != nil {
			serveFailure(response, http.StatusForbidden, "request verification failed")
			return
		}
		form, parseErr := parsePublishingForm(request, false)
		if parseErr != nil {
			serveFailure(response, http.StatusBadRequest, "invalid publishing form")
			return
		}
		cancelURL, buildErr := builder.Path("areas", form.AreaSlug)
		if buildErr != nil {
			serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
			return
		}
		result, publishErr := createTopic(request.Context(), access, form.AreaSlug, form.Title, form.Markdown)
		if publishErr != nil {
			if invalid, validation := publishingValidation(publishErr); validation {
				form.Heading, form.ActionURL, form.PreviewURL, form.CancelURL = "New topic", topicAction, topicPreviewAction, cancelURL
				form.CSRFToken = csrfTokenFromContext(request.Context())
				applyPublishingValidation(&form, invalid)
				renderForm(response, request, http.StatusUnprocessableEntity, form)
				return
			}
			servePublishingError(response, publishErr)
			return
		}
		if result.TopicID <= 0 || result.PostID <= 0 || result.PostNumber != 1 || result.NodeOrdinal != 1 {
			serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
			return
		}
		location, buildErr := builder.Path("topics", strconv.FormatInt(result.TopicID, 10))
		if buildErr != nil {
			serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
			return
		}
		serveMutationNavigation(response, request, location)
	})
	router.Post("/topics/{topicID}/replies/preview", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		_, redirect := authorized(request)
		if redirect != "" {
			serveSessionRedirect(response, request, redirect)
			return
		}
		topicID, identifierErr := parseTopicID(chi.URLParam(request, "topicID"))
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || identifierErr != nil {
			serveFailure(response, http.StatusNotFound, "page not found")
			return
		}
		if err := validateCSRFRequest(request, maximumPublishingFormBytes); err != nil {
			serveFailure(response, http.StatusForbidden, "request verification failed")
			return
		}
		form, parseErr := parsePublishingForm(request, true)
		if parseErr != nil {
			serveFailure(response, http.StatusBadRequest, "invalid preview form")
			return
		}
		topicIdentifier := strconv.FormatInt(topicID, 10)
		topicURL, buildErr := builder.Path("topics", topicIdentifier)
		if buildErr != nil {
			serveFailure(response, http.StatusServiceUnavailable, "preview unavailable")
			return
		}
		previewURL, buildErr := builder.Path("topics", topicIdentifier, "replies", "preview")
		if buildErr != nil {
			serveFailure(response, http.StatusServiceUnavailable, "preview unavailable")
			return
		}
		actionURL, buildErr := builder.Path("topics", topicIdentifier, "replies")
		if buildErr != nil {
			serveFailure(response, http.StatusServiceUnavailable, "preview unavailable")
			return
		}
		form.Heading, form.ActionURL, form.PreviewURL, form.CancelURL, form.Reply = "Reply", actionURL, previewURL, topicURL, true
		form.CSRFToken = csrfTokenFromContext(request.Context())
		rendered, previewErr := forum.RenderReplyDraft(form.Markdown)
		if previewErr != nil {
			if invalid, validation := publishingValidation(previewErr); validation {
				applyPublishingValidation(&form, invalid)
				renderForm(response, request, http.StatusUnprocessableEntity, form)
				return
			}
			serveFailure(response, http.StatusServiceUnavailable, "preview unavailable")
			return
		}
		form.PreviewBody, form.ShowPreview = rendered.TrustedHTML(), true
		renderForm(response, request, http.StatusOK, form)
	})
	router.Post("/topics/{topicID}/replies", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request)
		if redirect != "" {
			serveSessionRedirect(response, request, redirect)
			return
		}
		topicID, identifierErr := parseTopicID(chi.URLParam(request, "topicID"))
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || identifierErr != nil {
			serveFailure(response, http.StatusNotFound, "page not found")
			return
		}
		if err := validateCSRFRequest(request, maximumPublishingFormBytes); err != nil {
			serveFailure(response, http.StatusForbidden, "request verification failed")
			return
		}
		form, parseErr := parsePublishingForm(request, true)
		if parseErr != nil {
			serveFailure(response, http.StatusBadRequest, "invalid publishing form")
			return
		}
		topicIdentifier := strconv.FormatInt(topicID, 10)
		topicURL, buildErr := builder.Path("topics", topicIdentifier)
		if buildErr != nil {
			serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
			return
		}
		parentPostID, parseParentErr := strconv.ParseInt(form.ParentPostID, 10, 64)
		if parseParentErr != nil || parentPostID <= 0 || strconv.FormatInt(parentPostID, 10) != form.ParentPostID {
			serveFailure(response, http.StatusBadRequest, "invalid publishing form")
			return
		}
		result, publishErr := createReply(request.Context(), access, topicID, parentPostID, form.Markdown)
		if publishErr != nil {
			if invalid, validation := publishingValidation(publishErr); validation {
				actionURL, actionErr := builder.Path("topics", topicIdentifier, "replies")
				if actionErr != nil {
					serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
					return
				}
				previewURL, previewErr := builder.Path("topics", topicIdentifier, "replies", "preview")
				if previewErr != nil {
					serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
					return
				}
				form.Heading, form.ActionURL, form.PreviewURL, form.CancelURL, form.Reply = "Reply", actionURL, previewURL, topicURL, true
				form.CSRFToken = csrfTokenFromContext(request.Context())
				applyPublishingValidation(&form, invalid)
				renderForm(response, request, http.StatusUnprocessableEntity, form)
				return
			}
			servePublishingError(response, publishErr)
			return
		}
		if result.TopicID != topicID || result.PostID <= 0 || result.PostNumber < 2 || result.NodeOrdinal < 2 {
			serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
			return
		}
		page := 1 + (result.NodeOrdinal-1)/int64(store.PostPageSize)
		query := url.Values(nil)
		if page > 1 {
			query = url.Values{"page": {strconv.FormatInt(int64(page), 10)}}
		}
		location, buildErr := builder.PathWithQueryAndFragment(
			[]string{"topics", topicIdentifier}, query, "post-"+strconv.FormatInt(result.PostID, 10),
		)
		if buildErr != nil {
			serveFailure(response, http.StatusServiceUnavailable, "publishing unavailable")
			return
		}
		serveMutationNavigation(response, request, location)
	})
	return recordRoutePattern(router), nil
}

// parseNewTopicArea accepts only one canonical area query.
//
// Complexity: for q bounded query bytes, time and auxiliary space are O(q),
// Omega(1), delegated to URL decoding. The caller's HTTP limits bound q.
func parseNewTopicArea(rawQuery string) (string, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil || len(values) != 1 || len(values["area"]) != 1 || values.Encode() != rawQuery || !policy.ValidAreaSlug(values.Get("area")) {
		return "", fmt.Errorf("new-topic area query is invalid")
	}
	return values.Get("area"), nil
}

// parsePublishingForm reads only the already-CSRF-bounded restored form and
// rejects duplicate, missing, or unknown fields.
//
// Complexity: for n body bytes and f fields, time and auxiliary space are
// O(n+f), Omega(n), delegated to net/http form parsing. No input is copied
// again by this function after parsing.
func parsePublishingForm(request *http.Request, reply bool) (publishingFormView, error) {
	if err := request.ParseForm(); err != nil {
		return publishingFormView{}, fmt.Errorf("parse publishing form: %w", err)
	}
	allowed := map[string]bool{"_csrf": true, "markdown": true}
	if reply {
		allowed["parent_post_id"] = true
	} else {
		allowed["area"], allowed["title"] = true, true
	}
	for key, values := range request.PostForm {
		if !allowed[key] || len(values) != 1 {
			return publishingFormView{}, fmt.Errorf("publishing form field is missing, duplicated, or unknown")
		}
	}
	if len(request.PostForm["markdown"]) != 1 || reply && len(request.PostForm["parent_post_id"]) != 1 || !reply && (len(request.PostForm["area"]) != 1 || len(request.PostForm["title"]) != 1) {
		return publishingFormView{}, fmt.Errorf("publishing form field is missing, duplicated, or unknown")
	}
	if reply {
		parent := request.PostForm.Get("parent_post_id")
		identifier, err := strconv.ParseInt(parent, 10, 64)
		if err != nil || identifier <= 0 || strconv.FormatInt(identifier, 10) != parent {
			return publishingFormView{}, fmt.Errorf("publishing reply parent is invalid")
		}
	}
	return publishingFormView{
		AreaSlug: request.PostForm.Get("area"), Title: request.PostForm.Get("title"),
		ParentPostID: request.PostForm.Get("parent_post_id"), Markdown: request.PostForm.Get("markdown"), Reply: reply,
	}, nil
}

// publishingValidation extracts only the safe typed field classification.
//
// Complexity: error-chain traversal is O(e), Omega(1), for e wrapped errors;
// auxiliary space is tight Theta(1).
func publishingValidation(err error) (forum.InvalidPublishingInput, bool) {
	var invalid forum.InvalidPublishingInput
	ok := errors.As(err, &invalid)
	return invalid, ok
}

// applyPublishingValidation maps one service-owned field name to escaped form
// presentation without retaining any error or submitted secret.
//
// Complexity: time and auxiliary space are tight Theta(1).
func applyPublishingValidation(form *publishingFormView, invalid forum.InvalidPublishingInput) {
	switch invalid.Field {
	case "title":
		form.TitleError = "Check the topic title."
	case "markdown":
		form.MarkdownError = "Check the Markdown body."
	default:
		form.FormError = "Check the submitted fields."
	}
}

// servePublishingError maps stable service/storage error classes to generic
// non-cacheable HTTP failures without exposing persistence diagnostics.
//
// Complexity: wrapped-error traversal is O(e), Omega(1), for e error nodes;
// response work is fixed-size apart from delegated writer I/O.
func servePublishingError(response http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "publishing unavailable"
	if errors.Is(err, forum.ErrPublishingDenied) {
		status, message = http.StatusForbidden, "publishing denied"
	} else if errors.Is(err, pgx.ErrNoRows) {
		status, message = http.StatusNotFound, "page not found"
	}
	http.Error(response, message, status)
}
