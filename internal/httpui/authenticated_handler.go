package httpui

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/auth"
)

const maxLogoutFormBytes = 4096

type userModerationLinksContextKey struct{}

// AuthenticationService is the exact initial-login and local-session surface
// consumed by the browser router.
type AuthenticationService interface {
	BeginInitialLogin(context.Context, string) (string, string, error)
	BeginRevalidation(context.Context, int64, string) (string, string, error)
	CompleteInitialLogin(context.Context, string, string) (string, string, time.Time, error)
	CompleteRevalidation(context.Context, string, string, string) (string, string, time.Time, error)
	AuthenticateSession(context.Context, string) (auth.SessionAuthentication, error)
	RevokeSession(context.Context, string) (bool, error)
}

// NewAuthenticatedHandler activates the login, callback, authenticated shell,
// and local logout boundaries around the public router. Only the exact forum
// root, one-segment GET area routes, canonical positive-decimal one-segment GET
// topic routes, revalidation, and logout perform session lookup;
// infrastructure, malformed/noncanonical read paths, wrong methods, and
// unknown paths remain usable when the session store is unavailable.
//
// Complexity: construction is tight Theta(1) time and auxiliary space around
// fixed handler state. For path bytes p and delegated handler cost D, request
// dispatch is O(p+D) time and Omega(1); local auxiliary space is tight
// Theta(1), plus space owned by the delegated handler. Area/topic prefix and
// segment checks scan p, and canonical numeric topic parsing scans at most 19
// bytes. OIDC, PostgreSQL, cookie, CSRF, template, and transport costs retain
// their documented bounds. No operation is retried or detached.
func NewAuthenticatedHandler(
	builder URLBuilder,
	service AuthenticationService,
	listAreas AreaIndexLister,
	loadAreaTopics AreaTopicPageLoader,
	maximumTopicPage int32,
	loadTopicPosts TopicPostPageLoader,
	maximumPostPage int32,
	sessionCookieName string,
	secure bool,
) (http.Handler, error) {
	return newAuthenticatedHandler(
		builder, service, listAreas, loadAreaTopics, maximumTopicPage, loadTopicPosts, maximumPostPage,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, sessionCookieName, secure,
	)
}

// NewAuthenticatedPublishingHandler constructs the complete alpha browser
// boundary with authenticated topic/reply publication added to the existing
// read, login, revalidation, and logout routes.
//
// Complexity: construction and dispatch retain NewAuthenticatedHandler's
// bounds; the publishing delegates add only their documented bounded form,
// rendering, transaction, and response costs. No operation is retried or
// detached.
func NewAuthenticatedPublishingHandler(
	builder URLBuilder,
	service AuthenticationService,
	listAreas AreaIndexLister,
	loadAreaTopics AreaTopicPageLoader,
	maximumTopicPage int32,
	loadTopicPosts TopicPostPageLoader,
	maximumPostPage int32,
	createTopic TopicPublisher,
	createReply ReplyPublisher,
	sessionCookieName string,
	secure bool,
) (http.Handler, error) {
	if createTopic == nil || createReply == nil {
		return nil, fmt.Errorf("browser publishing services are required")
	}
	return newAuthenticatedHandler(
		builder, service, listAreas, loadAreaTopics, maximumTopicPage, loadTopicPosts, maximumPostPage,
		createTopic, createReply, nil, nil, nil, nil, nil, nil, nil, sessionCookieName, secure,
	)
}

// NewAuthenticatedForumHandler constructs the current alpha browser boundary
// with forum reads, publication, preview, author editing, and soft deletion.
//
// Complexity: construction and dispatch retain NewAuthenticatedHandler's
// bounds; delegated publishing/editing services retain their own bounded
// contracts. No operation is retried or detached.
func NewAuthenticatedForumHandler(
	builder URLBuilder,
	service AuthenticationService,
	listAreas AreaIndexLister,
	loadAreaTopics AreaTopicPageLoader,
	maximumTopicPage int32,
	loadTopicPosts TopicPostPageLoader,
	maximumPostPage int32,
	createTopic TopicPublisher,
	createReply ReplyPublisher,
	loadEditablePost EditablePostLoader,
	editPost PostEditor,
	deletePost PostDeleter,
	sessionCookieName string,
	secure bool,
) (http.Handler, error) {
	if createTopic == nil || createReply == nil || loadEditablePost == nil || editPost == nil || deletePost == nil {
		return nil, fmt.Errorf("browser forum services are required")
	}
	return newAuthenticatedHandler(
		builder, service, listAreas, loadAreaTopics, maximumTopicPage, loadTopicPosts, maximumPostPage,
		createTopic, createReply, loadEditablePost, editPost, deletePost, nil, nil, nil, nil, sessionCookieName, secure,
	)
}

// NewAuthenticatedModeratedForumHandler constructs the alpha browser boundary
// with the complete forum surface plus staff topic lock/unlock, hide/restore,
// account-status, and account suspend/reinstate controls.
//
// Complexity: construction and dispatch retain NewAuthenticatedHandler's
// bounds; delegated publishing, editing, and moderation services retain their
// own bounded contracts. No operation is retried or detached.
func NewAuthenticatedModeratedForumHandler(
	builder URLBuilder,
	service AuthenticationService,
	listAreas AreaIndexLister,
	loadAreaTopics AreaTopicPageLoader,
	maximumTopicPage int32,
	loadTopicPosts TopicPostPageLoader,
	maximumPostPage int32,
	createTopic TopicPublisher,
	createReply ReplyPublisher,
	loadEditablePost EditablePostLoader,
	editPost PostEditor,
	deletePost PostDeleter,
	changeTopicLock TopicLockChanger,
	changeTopicVisibility TopicVisibilityChanger,
	loadModerationUser ModerationUserStatusLoader,
	changeUserSuspension UserSuspensionChanger,
	sessionCookieName string,
	secure bool,
) (http.Handler, error) {
	if createTopic == nil || createReply == nil || loadEditablePost == nil || editPost == nil || deletePost == nil || changeTopicLock == nil || changeTopicVisibility == nil || loadModerationUser == nil || changeUserSuspension == nil {
		return nil, fmt.Errorf("browser moderated forum services are required")
	}
	return newAuthenticatedHandler(
		builder, service, listAreas, loadAreaTopics, maximumTopicPage, loadTopicPosts, maximumPostPage,
		createTopic, createReply, loadEditablePost, editPost, deletePost, changeTopicLock, changeTopicVisibility,
		loadModerationUser, changeUserSuspension, sessionCookieName, secure,
	)
}

// newAuthenticatedHandler owns the common browser construction and exact-path
// session dispatch for the read-only and publishing-enabled variants.
//
// Complexity: construction is tight Theta(1). For path bytes p and delegated
// work D, dispatch is O(p+D) time and tight Theta(1) local auxiliary space;
// canonical topic-moderation path recognition scans p at most four times,
// account-moderation recognition scans p at most twice, and reply recognition
// scans p at most twice. No request is retried.
func newAuthenticatedHandler(
	builder URLBuilder,
	service AuthenticationService,
	listAreas AreaIndexLister,
	loadAreaTopics AreaTopicPageLoader,
	maximumTopicPage int32,
	loadTopicPosts TopicPostPageLoader,
	maximumPostPage int32,
	createTopic TopicPublisher,
	createReply ReplyPublisher,
	loadEditablePost EditablePostLoader,
	editPost PostEditor,
	deletePost PostDeleter,
	changeTopicLock TopicLockChanger,
	changeTopicVisibility TopicVisibilityChanger,
	loadModerationUser ModerationUserStatusLoader,
	changeUserSuspension UserSuspensionChanger,
	sessionCookieName string,
	secure bool,
) (http.Handler, error) {
	if service == nil {
		return nil, fmt.Errorf("browser authentication service is required")
	}
	publicHandler, err := NewHandler(builder, listAreas, loadAreaTopics, maximumTopicPage, loadTopicPosts, maximumPostPage)
	if err != nil {
		return nil, fmt.Errorf("construct public browser routes: %w", err)
	}
	if loadModerationUser != nil && changeUserSuspension != nil {
		basePublicHandler := publicHandler
		publicHandler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			basePublicHandler.ServeHTTP(response, request.WithContext(context.WithValue(request.Context(), userModerationLinksContextKey{}, true)))
		})
	}
	loginHandler, err := newLoginStartHandler(
		service.BeginInitialLogin, initialLoginStateCookieSuffix, sessionCookieName, builder, secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct initial login route: %w", err)
	}
	revalidationHandler, err := newLoginStartHandler(
		func(ctx context.Context, returnPath string) (string, string, error) {
			authentication := sessionAuthenticationFromContext(ctx)
			if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
				return "", "", fmt.Errorf("authenticated session is required for revalidation")
			}
			return service.BeginRevalidation(ctx, authentication.SessionID, returnPath)
		},
		revalidationStateCookieSuffix,
		sessionCookieName,
		builder,
		secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct revalidation route: %w", err)
	}
	authorizedRevalidationHandler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		authentication := sessionAuthenticationFromContext(request.Context())
		if !authentication.Access.Authenticated || authentication.SessionID <= 0 {
			response.Header().Set("Cache-Control", "no-store")
			http.Error(response, "authentication required", http.StatusUnauthorized)
			return
		}
		revalidationHandler.ServeHTTP(response, request)
	})
	callbackHandler, err := newAuthenticationCallbackHandler(
		func(ctx context.Context, state, code string) (completedBrowserLogin, error) {
			token, returnPath, expiresAt, completionErr := service.CompleteInitialLogin(ctx, state, code)
			return completedBrowserLogin{token: token, returnPath: returnPath, expiresAt: expiresAt}, completionErr
		},
		func(ctx context.Context, state, code, oldToken string) (completedBrowserLogin, error) {
			token, returnPath, expiresAt, completionErr := service.CompleteRevalidation(ctx, state, code, oldToken)
			return completedBrowserLogin{token: token, returnPath: returnPath, expiresAt: expiresAt}, completionErr
		},
		sessionCookieName,
		builder,
		secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct initial login callback route: %w", err)
	}
	logoutHandler, err := newLogoutHandler(
		service.RevokeSession,
		func(request *http.Request) error { return validateCSRFRequest(request, maxLogoutFormBytes) },
		sessionCookieName,
		builder,
		secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct logout route: %w", err)
	}
	authenticatedPublicHandler, err := newSessionAuthenticationHandler(publicHandler, service.AuthenticateSession, sessionCookieName, builder, secure)
	if err != nil {
		return nil, fmt.Errorf("construct browser session boundary: %w", err)
	}
	authenticatedLogoutHandler, err := newSessionAuthenticationHandler(logoutHandler, service.AuthenticateSession, sessionCookieName, builder, secure)
	if err != nil {
		return nil, fmt.Errorf("construct logout session boundary: %w", err)
	}
	authenticatedRevalidationHandler, err := newSessionAuthenticationHandler(
		authorizedRevalidationHandler, service.AuthenticateSession, sessionCookieName, builder, secure,
	)
	if err != nil {
		return nil, fmt.Errorf("construct revalidation session boundary: %w", err)
	}
	var authenticatedPublishingHandler http.Handler
	if createTopic != nil || createReply != nil {
		if createTopic == nil || createReply == nil {
			return nil, fmt.Errorf("browser publishing services are incomplete")
		}
		publishingHandler, publishingErr := newPublishingHandler(builder, createTopic, createReply)
		if publishingErr != nil {
			return nil, fmt.Errorf("construct publishing routes: %w", publishingErr)
		}
		authenticatedPublishingHandler, publishingErr = newSessionAuthenticationHandler(
			publishingHandler, service.AuthenticateSession, sessionCookieName, builder, secure,
		)
		if publishingErr != nil {
			return nil, fmt.Errorf("construct publishing session boundary: %w", publishingErr)
		}
	}
	var authenticatedEditingHandler http.Handler
	if loadEditablePost != nil || editPost != nil || deletePost != nil {
		if loadEditablePost == nil || editPost == nil || deletePost == nil {
			return nil, fmt.Errorf("browser editing services are incomplete")
		}
		editingHandler, editingErr := newEditingHandler(builder, loadEditablePost, editPost, deletePost)
		if editingErr != nil {
			return nil, fmt.Errorf("construct editing routes: %w", editingErr)
		}
		authenticatedEditingHandler, editingErr = newSessionAuthenticationHandler(
			editingHandler, service.AuthenticateSession, sessionCookieName, builder, secure,
		)
		if editingErr != nil {
			return nil, fmt.Errorf("construct editing session boundary: %w", editingErr)
		}
	}
	var authenticatedModerationHandler http.Handler
	if changeTopicLock != nil || changeTopicVisibility != nil {
		if changeTopicLock == nil || changeTopicVisibility == nil {
			return nil, fmt.Errorf("browser moderation services are incomplete")
		}
		moderationHandler, moderationErr := newModerationHandler(builder, changeTopicLock, changeTopicVisibility)
		if moderationErr != nil {
			return nil, fmt.Errorf("construct moderation routes: %w", moderationErr)
		}
		authenticatedModerationHandler, moderationErr = newSessionAuthenticationHandler(
			moderationHandler, service.AuthenticateSession, sessionCookieName, builder, secure,
		)
		if moderationErr != nil {
			return nil, fmt.Errorf("construct moderation session boundary: %w", moderationErr)
		}
	}
	var authenticatedUserModerationHandler http.Handler
	if loadModerationUser != nil || changeUserSuspension != nil {
		if loadModerationUser == nil || changeUserSuspension == nil {
			return nil, fmt.Errorf("browser user moderation services are incomplete")
		}
		userModerationHandler, moderationErr := newUserModerationHandler(builder, loadModerationUser, changeUserSuspension)
		if moderationErr != nil {
			return nil, fmt.Errorf("construct user moderation routes: %w", moderationErr)
		}
		authenticatedUserModerationHandler, moderationErr = newSessionAuthenticationHandler(
			userModerationHandler, service.AuthenticateSession, sessionCookieName, builder, secure,
		)
		if moderationErr != nil {
			return nil, fmt.Errorf("construct user moderation session boundary: %w", moderationErr)
		}
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/login":
			request.Pattern = request.Method + " /login"
			loginHandler.ServeHTTP(response, request)
		case "/auth/callback":
			request.Pattern = request.Method + " /auth/callback"
			callbackHandler.ServeHTTP(response, request)
		case "/auth/revalidate":
			request.Pattern = request.Method + " /auth/revalidate"
			authenticatedRevalidationHandler.ServeHTTP(response, request)
		case "/logout":
			request.Pattern = request.Method + " /logout"
			authenticatedLogoutHandler.ServeHTTP(response, request)
		case "/topics/new", "/topics", "/topics/preview":
			if authenticatedPublishingHandler != nil {
				authenticatedPublishingHandler.ServeHTTP(response, request)
				return
			}
			publicHandler.ServeHTTP(response, request)
		case "/":
			authenticatedPublicHandler.ServeHTTP(response, request)
		default:
			if authenticatedUserModerationHandler != nil && request.URL.RawPath == "" &&
				(request.Method == http.MethodGet || request.Method == http.MethodPost) {
				identifierAndSuffix, userPath := strings.CutPrefix(request.URL.Path, "/moderation/users/")
				identifier := identifierAndSuffix
				validAction := request.Method == http.MethodGet
				if request.Method == http.MethodPost {
					validAction = false
					for _, suffix := range [...]string{"/suspend", "/reinstate"} {
						if identifier, validAction = strings.CutSuffix(identifierAndSuffix, suffix); validAction {
							break
						}
					}
				}
				if userPath && validAction && identifier != "" && !strings.ContainsRune(identifier, '/') {
					if _, identifierErr := parseUserID(identifier); identifierErr == nil {
						authenticatedUserModerationHandler.ServeHTTP(response, request)
						return
					}
				}
			}
			if authenticatedModerationHandler != nil && request.Method == http.MethodPost && request.URL.RawPath == "" {
				identifierAndSuffix, topicPath := strings.CutPrefix(request.URL.Path, "/topics/")
				identifier, moderationPath := "", false
				for _, suffix := range [...]string{"/lock", "/unlock", "/hide", "/restore"} {
					if identifier, moderationPath = strings.CutSuffix(identifierAndSuffix, suffix); moderationPath {
						break
					}
				}
				if topicPath && moderationPath && identifier != "" && !strings.ContainsRune(identifier, '/') {
					if _, identifierErr := parseTopicID(identifier); identifierErr == nil {
						authenticatedModerationHandler.ServeHTTP(response, request)
						return
					}
				}
			}
			if authenticatedEditingHandler != nil && request.URL.RawPath == "" &&
				(request.Method == http.MethodGet || request.Method == http.MethodPost) {
				identifierAndSuffix, postPath := strings.CutPrefix(request.URL.Path, "/posts/")
				identifier, editPath := strings.CutSuffix(identifierAndSuffix, "/edit")
				if !editPath && request.Method == http.MethodPost {
					identifier, editPath = strings.CutSuffix(identifierAndSuffix, "/edit/preview")
					if !editPath {
						identifier, editPath = strings.CutSuffix(identifierAndSuffix, "/delete")
					}
				}
				if postPath && editPath && identifier != "" && !strings.ContainsRune(identifier, '/') {
					if _, identifierErr := parsePostID(identifier); identifierErr == nil {
						authenticatedEditingHandler.ServeHTTP(response, request)
						return
					}
				}
			}
			if authenticatedPublishingHandler != nil && request.Method == http.MethodPost && request.URL.RawPath == "" {
				identifierAndSuffix, topicPath := strings.CutPrefix(request.URL.Path, "/topics/")
				identifier, replyPath := strings.CutSuffix(identifierAndSuffix, "/replies")
				if !replyPath {
					identifier, replyPath = strings.CutSuffix(identifierAndSuffix, "/replies/preview")
				}
				if topicPath && replyPath && identifier != "" && !strings.ContainsRune(identifier, '/') {
					if _, identifierErr := parseTopicID(identifier); identifierErr == nil {
						authenticatedPublishingHandler.ServeHTTP(response, request)
						return
					}
				}
			}
			if request.Method == http.MethodGet && request.URL.RawPath == "" {
				slug, areaPath := strings.CutPrefix(request.URL.Path, "/areas/")
				if areaPath && slug != "" && !strings.ContainsRune(slug, '/') {
					authenticatedPublicHandler.ServeHTTP(response, request)
					return
				}
				identifier, topicPath := strings.CutPrefix(request.URL.Path, "/topics/")
				if topicPath && identifier != "" && !strings.ContainsRune(identifier, '/') {
					if _, identifierErr := parseTopicID(identifier); identifierErr != nil {
						publicHandler.ServeHTTP(response, request)
						return
					}
					authenticatedPublicHandler.ServeHTTP(response, request)
					return
				}
			}
			publicHandler.ServeHTTP(response, request)
		}
	}), nil
}
