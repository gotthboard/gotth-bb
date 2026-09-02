package httpui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
	"git.dannyhunn.com/agents/gotth-bb/internal/forum"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type EditablePostLoader func(context.Context, auth.AccessContext, int64) (store.EditablePost, error)
type PostEditor func(context.Context, auth.AccessContext, int64, int32, string) (forum.EditResult, error)
type PostDeleter func(context.Context, auth.AccessContext, int64, int32) (forum.DeleteResult, error)

const maximumEditFormRevision = int64(1<<31 - 1)
const maximumDeleteFormRevision = int64(1 << 31)

// newEditingHandler constructs authenticated author edit, preview, apply, and
// soft-delete routes. GET and preview use read authority for presentation;
// writes delegate final authority to their locked transactions.
//
// Complexity: construction is tight Theta(1). For bounded form bytes
// n <= 262,144, delegated work D, and response bytes r, request time is
// O(n+D+r), Omega(1), and auxiliary space is O(n+r), Omega(1), without a tight
// bound because PostgreSQL, rendering, and writer work vary. Each delegate is
// called at most once on success paths and no work is retried or detached.
func newEditingHandler(builder URLBuilder, load EditablePostLoader, edit PostEditor, deletePost PostDeleter) (http.Handler, error) {
	if load == nil {
		return nil, fmt.Errorf("editable post loader is required")
	}
	if edit == nil {
		return nil, fmt.Errorf("post editor is required")
	}
	if deletePost == nil {
		return nil, fmt.Errorf("post deleter is required")
	}
	loginURL, err := builder.Path("login")
	if err != nil {
		return nil, fmt.Errorf("build editing login URL: %w", err)
	}
	revalidationURL, err := builder.Path("auth", "revalidate")
	if err != nil {
		return nil, fmt.Errorf("build editing revalidation URL: %w", err)
	}
	baseView, err := newPageView(builder, "Edit post")
	if err != nil {
		return nil, fmt.Errorf("construct editing view: %w", err)
	}
	baseView.CanonicalURL = ""

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
	renderForm := func(response http.ResponseWriter, request *http.Request, status int, form publishingFormView) {
		view := baseView
		if renderErr := renderResponse(response, request, status, publishingFormPage(view, form), publishingFormContent(view, form)); renderErr != nil {
			panic(renderErr)
		}
	}
	buildForm := func(postID int64, loaded store.EditablePost, markdown, revision, csrf string) (publishingFormView, error) {
		identifier := strconv.FormatInt(postID, 10)
		actionURL, buildErr := builder.Path("posts", identifier, "edit")
		if buildErr != nil {
			return publishingFormView{}, buildErr
		}
		previewURL, buildErr := builder.Path("posts", identifier, "edit", "preview")
		if buildErr != nil {
			return publishingFormView{}, buildErr
		}
		page := 1 + (loaded.PostNumber-1)/store.PostPageSize
		query := url.Values(nil)
		if page > 1 {
			query = url.Values{"page": {strconv.FormatInt(int64(page), 10)}}
		}
		cancelURL, buildErr := builder.PathWithQueryAndFragment(
			[]string{"topics", strconv.FormatInt(loaded.TopicID, 10)}, query, "post-"+identifier,
		)
		if buildErr != nil {
			return publishingFormView{}, buildErr
		}
		return publishingFormView{
			Heading: "Edit post", ActionURL: actionURL, PreviewURL: previewURL, CancelURL: cancelURL,
			CSRFToken: csrf, Markdown: markdown, Edit: true, Revision: revision,
		}, nil
	}
	loadForm := func(request *http.Request, access auth.AccessContext, postID int64, markdown, revision string, submitted bool) (publishingFormView, error) {
		loaded, loadErr := load(request.Context(), access, postID)
		if loadErr != nil {
			return publishingFormView{}, loadErr
		}
		if loaded.PostID != postID || loaded.TopicID <= 0 || loaded.PostNumber <= 0 || loaded.Revision <= 0 {
			return publishingFormView{}, fmt.Errorf("editable post loader returned an invalid result")
		}
		if !submitted {
			markdown = loaded.MarkdownSource
			revision = strconv.FormatInt(int64(loaded.Revision), 10)
		}
		return buildForm(postID, loaded, markdown, revision, csrfTokenFromContext(request.Context()))
	}
	serveError := func(response http.ResponseWriter, editErr error) {
		status, message := http.StatusServiceUnavailable, "editing unavailable"
		if errors.Is(editErr, forum.ErrPostEditDenied) || errors.Is(editErr, forum.ErrPostDeleteDenied) {
			status, message = http.StatusForbidden, "editing denied"
		} else if errors.Is(editErr, pgx.ErrNoRows) {
			status, message = http.StatusNotFound, "page not found"
		}
		http.Error(response, message, status)
	}

	router := chi.NewRouter()
	router.Use(captureRoutePattern)
	router.Get("/posts/{postID}/edit", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request)
		if redirect != "" {
			servePublishingRedirect(response, request, redirect)
			return
		}
		postID, parseErr := parsePostID(chi.URLParam(request, "postID"))
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || parseErr != nil {
			http.Error(response, "page not found", http.StatusNotFound)
			return
		}
		if len(csrfTokenFromContext(request.Context())) != sessionCookieEncodedBytes {
			http.Error(response, "editing unavailable", http.StatusServiceUnavailable)
			return
		}
		form, formErr := loadForm(request, access, postID, "", "", false)
		if formErr != nil {
			serveError(response, formErr)
			return
		}
		renderForm(response, request, http.StatusOK, form)
	})
	router.Post("/posts/{postID}/edit/preview", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request)
		if redirect != "" {
			servePublishingRedirect(response, request, redirect)
			return
		}
		postID, parseErr := parsePostID(chi.URLParam(request, "postID"))
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || parseErr != nil {
			http.Error(response, "page not found", http.StatusNotFound)
			return
		}
		if csrfErr := validateCSRFRequest(request, maximumPublishingFormBytes); csrfErr != nil {
			http.Error(response, "request verification failed", http.StatusForbidden)
			return
		}
		markdown, revision, formErr := parseEditForm(request)
		if formErr != nil {
			http.Error(response, "invalid edit form", http.StatusBadRequest)
			return
		}
		form, formErr := loadForm(request, access, postID, markdown, strconv.FormatInt(int64(revision), 10), true)
		if formErr != nil {
			serveError(response, formErr)
			return
		}
		rendered, previewErr := forum.RenderReplyDraft(markdown)
		if previewErr != nil {
			if invalid, validation := publishingValidation(previewErr); validation {
				applyPublishingValidation(&form, invalid)
				renderForm(response, request, http.StatusUnprocessableEntity, form)
				return
			}
			serveError(response, previewErr)
			return
		}
		form.PreviewBody, form.ShowPreview = rendered.TrustedHTML(), true
		renderForm(response, request, http.StatusOK, form)
	})
	router.Post("/posts/{postID}/edit", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request)
		if redirect != "" {
			servePublishingRedirect(response, request, redirect)
			return
		}
		postID, parseErr := parsePostID(chi.URLParam(request, "postID"))
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || parseErr != nil {
			http.Error(response, "page not found", http.StatusNotFound)
			return
		}
		if csrfErr := validateCSRFRequest(request, maximumPublishingFormBytes); csrfErr != nil {
			http.Error(response, "request verification failed", http.StatusForbidden)
			return
		}
		markdown, revision, formErr := parseEditForm(request)
		if formErr != nil {
			http.Error(response, "invalid edit form", http.StatusBadRequest)
			return
		}
		result, editErr := edit(request.Context(), access, postID, revision, markdown)
		if editErr != nil {
			invalid, validation := publishingValidation(editErr)
			if validation || errors.Is(editErr, forum.ErrPostEditConflict) {
				form, loadErr := loadForm(request, access, postID, markdown, strconv.FormatInt(int64(revision), 10), true)
				if loadErr != nil {
					serveError(response, loadErr)
					return
				}
				status := http.StatusUnprocessableEntity
				if validation {
					applyPublishingValidation(&form, invalid)
				} else {
					status = http.StatusConflict
					form.FormError = "This post changed since you opened it. Reload and retry."
				}
				renderForm(response, request, status, form)
				return
			}
			serveError(response, editErr)
			return
		}
		if result.PostID != postID || result.TopicID <= 0 || result.PostNumber <= 0 || result.Revision != revision+1 {
			http.Error(response, "editing unavailable", http.StatusServiceUnavailable)
			return
		}
		page := 1 + (result.PostNumber-1)/store.PostPageSize
		query := url.Values(nil)
		if page > 1 {
			query = url.Values{"page": {strconv.FormatInt(int64(page), 10)}}
		}
		location, buildErr := builder.PathWithQueryAndFragment(
			[]string{"topics", strconv.FormatInt(result.TopicID, 10)}, query, "post-"+strconv.FormatInt(postID, 10),
		)
		if buildErr != nil {
			http.Error(response, "editing unavailable", http.StatusServiceUnavailable)
			return
		}
		servePublishingRedirect(response, request, location)
	})
	router.Post("/posts/{postID}/delete", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		access, redirect := authorized(request)
		if redirect != "" {
			servePublishingRedirect(response, request, redirect)
			return
		}
		postID, parseErr := parsePostID(chi.URLParam(request, "postID"))
		if request.URL.RawPath != "" || request.URL.RawQuery != "" || parseErr != nil {
			http.Error(response, "page not found", http.StatusNotFound)
			return
		}
		if csrfErr := validateCSRFRequest(request, maximumPublishingFormBytes); csrfErr != nil {
			http.Error(response, "request verification failed", http.StatusForbidden)
			return
		}
		revision, formErr := parseDeleteForm(request)
		if formErr != nil {
			http.Error(response, "invalid delete form", http.StatusBadRequest)
			return
		}
		result, deleteErr := deletePost(request.Context(), access, postID, revision)
		if deleteErr != nil {
			if errors.Is(deleteErr, forum.ErrPostDeleteConflict) {
				http.Error(response, "post changed; reload before deleting", http.StatusConflict)
				return
			}
			serveError(response, deleteErr)
			return
		}
		if result.PostID != postID || result.TopicID <= 0 || result.PostNumber <= 0 || result.Revision != revision {
			http.Error(response, "editing unavailable", http.StatusServiceUnavailable)
			return
		}
		location, buildErr := builder.Path("topics", strconv.FormatInt(result.TopicID, 10))
		if buildErr != nil {
			http.Error(response, "editing unavailable", http.StatusServiceUnavailable)
			return
		}
		servePublishingRedirect(response, request, location)
	})
	return recordRoutePattern(router), nil
}

// parseEditForm reads the already-CSRF-bounded form and accepts only one
// canonical positive int32 revision plus one Markdown source.
//
// Complexity: for n body bytes and f fields, time and auxiliary space are
// O(n+f), Omega(n), delegated to net/http form parsing.
func parseEditForm(request *http.Request) (string, int32, error) {
	if err := request.ParseForm(); err != nil {
		return "", 0, fmt.Errorf("parse edit form: %w", err)
	}
	allowed := map[string]bool{"_csrf": true, "markdown": true, "revision": true}
	for key, values := range request.PostForm {
		if !allowed[key] || len(values) != 1 {
			return "", 0, fmt.Errorf("edit form field is missing, duplicated, or unknown")
		}
	}
	if len(request.PostForm["markdown"]) != 1 || len(request.PostForm["revision"]) != 1 {
		return "", 0, fmt.Errorf("edit form field is missing, duplicated, or unknown")
	}
	rawRevision := request.PostForm.Get("revision")
	parsedRevision, err := parseCanonicalRevision(rawRevision, maximumEditFormRevision)
	if err != nil {
		return "", 0, fmt.Errorf("edit revision is invalid")
	}
	return request.PostForm.Get("markdown"), parsedRevision, nil
}

// parseDeleteForm accepts only the CSRF field and one canonical positive int32
// revision from an already bounded request.
//
// Complexity: for n body bytes and f fields, time and auxiliary space are
// O(n+f), Omega(n), delegated to net/http form parsing.
func parseDeleteForm(request *http.Request) (int32, error) {
	if err := request.ParseForm(); err != nil {
		return 0, fmt.Errorf("parse delete form: %w", err)
	}
	allowed := map[string]bool{"_csrf": true, "revision": true}
	for key, values := range request.PostForm {
		if !allowed[key] || len(values) != 1 {
			return 0, fmt.Errorf("delete form field is missing, duplicated, or unknown")
		}
	}
	if len(request.PostForm["revision"]) != 1 {
		return 0, fmt.Errorf("delete form field is missing, duplicated, or unknown")
	}
	revision, err := parseCanonicalRevision(request.PostForm.Get("revision"), maximumDeleteFormRevision)
	if err != nil {
		return 0, fmt.Errorf("delete revision is invalid")
	}
	return revision, nil
}

// parseCanonicalRevision accepts one positive canonical decimal int32 below an
// operation-specific exclusive maximum.
//
// Complexity: for n <= 10 input bytes, time is O(n), Omega(1), and auxiliary
// space is tight Theta(1).
func parseCanonicalRevision(raw string, exclusiveMaximum int64) (int32, error) {
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || parsed <= 0 || parsed >= exclusiveMaximum || strconv.FormatInt(parsed, 10) != raw {
		return 0, fmt.Errorf("revision is invalid")
	}
	return int32(parsed), nil
}
