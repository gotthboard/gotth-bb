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
	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	contentrender "git.dannyhunn.com/agents/gotth-bb/internal/render"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// TopicPostPageLoader returns one atomically access-filtered topic/post page
// for the canonical request authority, topic identifier, and page number.
type TopicPostPageLoader func(context.Context, auth.AccessContext, int64, int32) (store.VisibleTopicPostPage, error)

// newTopicPostListHandler parses canonical topic/page input, invokes one
// access-aware loader, projects only validated rows, re-sanitizes persisted
// renderer output into TrustedHTML, and renders full or HTMX topic pages.
// Missing and inaccessible input shares one 404; storage or malformed-result
// failures discard all partial presentation and return one redacted 503.
//
// Complexity: construction is tight Theta(1) time and space. For q bounded
// query/path bytes, r <= 25 post rows containing h rendered bytes, delegated
// loader cost L, and n response bytes, request time is O(q+r+h+L+n), Omega(1),
// without a tight Theta bound because PostgreSQL and writer work vary.
// Auxiliary space is O(r+h+n+A(L)), Omega(1), including trusted projections
// and the buffered render. No operation is retried or detached.
func newTopicPostListHandler(builder URLBuilder, maximumPage int32, load TopicPostPageLoader) (http.Handler, error) {
	baseView, err := newPageView(builder, "Topic")
	if err != nil {
		return nil, fmt.Errorf("construct topic post base view: %w", err)
	}
	if _, err := parseTopicPageQuery("", maximumPage); err != nil {
		return nil, fmt.Errorf("validate topic post page maximum: %w", err)
	}
	if load == nil {
		return nil, fmt.Errorf("topic post page loader is required")
	}
	notFoundView := baseView
	notFoundView.Title = "Page not found"
	notFoundView.CanonicalURL = ""
	unavailableView := baseView
	unavailableView.Title = "Topic unavailable"
	unavailableView.CanonicalURL = ""
	serveError := func(response http.ResponseWriter, request *http.Request, status int, view pageView, heading, message string) {
		if renderErr := renderResponse(
			response, request, status,
			errorPage(view, status, heading, message),
			errorContent(view, status, heading, message),
		); renderErr != nil {
			panic(renderErr)
		}
	}
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		topicID, identifierErr := parseTopicID(chi.URLParam(request, "topicID"))
		pageNumber, pageErr := parseTopicPageQuery(request.URL.RawQuery, maximumPage)
		if request.URL.RawPath != "" || identifierErr != nil || pageErr != nil {
			serveError(response, request, http.StatusNotFound, notFoundView, "Page not found", "The requested page does not exist or is not visible to you.")
			return
		}
		authentication := sessionAuthenticationFromContext(request.Context())
		loaded, loadErr := load(request.Context(), authentication.Access, topicID, pageNumber)
		if loadErr != nil {
			if errors.Is(loadErr, pgx.ErrNoRows) {
				serveError(response, request, http.StatusNotFound, notFoundView, "Page not found", "The requested page does not exist or is not visible to you.")
				return
			}
			serveError(response, request, http.StatusServiceUnavailable, unavailableView, "Topic unavailable", "This topic is temporarily unavailable.")
			return
		}
		if len(loaded.Rows) == 0 {
			serveError(response, request, http.StatusServiceUnavailable, unavailableView, "Topic unavailable", "This topic is temporarily unavailable.")
			return
		}
		first := loaded.Rows[0]
		expectedPages := int64(0)
		if loaded.TotalPosts > 0 {
			expectedPages = 1 + (loaded.TotalPosts-1)/int64(store.PostPageSize)
		}
		invalid := loaded.Number != pageNumber || loaded.TotalPosts < 0 || loaded.TotalPages != expectedPages ||
			first.AreaID <= 0 || !policy.ValidAreaSlug(first.AreaSlug) || first.AreaName == "" || first.TopicID != topicID ||
			(first.AreaPostingMode != "normal" && first.AreaPostingMode != "read_only" && first.AreaPostingMode != "archived") ||
			first.TopicTitle == "" || first.TopicAuthorDisplayName == "" || !first.TopicCreatedAt.Valid ||
			len(loaded.Rows) > int(store.PostPageSize) ||
			(loaded.TotalPosts == 0 && (pageNumber != 1 || len(loaded.Rows) != 1 || first.PostID.Valid || first.PostNumber.Valid || first.ParentPostID.Valid ||
				first.ThreadDepth != 0 || first.IsTombstone.Valid || first.NodeOrdinal.Valid ||
				first.RenderedHtml.Valid || first.RendererVersion.Valid || first.Revision.Valid || first.PostCreatedAt.Valid ||
				first.PostUpdatedAt.Valid || first.PostEditedAt.Valid || first.PostAuthorDisplayName.Valid ||
				first.TotalVisiblePosts != loaded.TotalPosts)) ||
			(loaded.TotalPosts > 0 && (int64(pageNumber) > loaded.TotalPages || !first.PostID.Valid))
		stateLabel := ""
		switch first.TopicState {
		case "open":
			stateLabel = "Open"
		case "locked":
			stateLabel = "Locked"
		case "archived":
			stateLabel = "Archived"
		case "hidden":
			stateLabel = "Hidden"
			invalid = invalid || authentication.Access.Role != auth.RoleModerator && authentication.Access.Role != auth.RoleAdministrator
		default:
			invalid = true
		}
		identifier := strconv.FormatInt(topicID, 10)
		segments := []string{"topics", identifier}
		view, viewErr := newPageView(builder, first.TopicTitle, segments...)
		areaURL := ""
		previousURL := ""
		nextURL := ""
		currentQuery := url.Values(nil)
		if pageNumber > 1 {
			currentQuery = url.Values{"page": {strconv.FormatInt(int64(pageNumber), 10)}}
		}
		if viewErr == nil && pageNumber > 1 {
			view.CanonicalURL, viewErr = builder.AbsoluteWithQuery(segments, currentQuery)
		}
		if viewErr == nil {
			areaURL, viewErr = builder.Path("areas", first.AreaSlug)
		}
		if viewErr == nil && pageNumber > 1 {
			previousPage := pageNumber - 1
			if previousPage == 1 {
				previousURL, viewErr = builder.Path(segments...)
			} else {
				previousURL, viewErr = builder.PathWithQuery(segments, url.Values{"page": {strconv.FormatInt(int64(previousPage), 10)}})
			}
		}
		if viewErr == nil && int64(pageNumber) < loaded.TotalPages && pageNumber < maximumPage {
			nextURL, viewErr = builder.PathWithQuery(segments, url.Values{"page": {strconv.FormatInt(int64(pageNumber+1), 10)}})
		}
		staff := authentication.Access.Role == auth.RoleModerator || authentication.Access.Role == auth.RoleAdministrator
		mayReply := authentication.Access.Authenticated && !authentication.Access.Suspended && authentication.Access.MutedUntil == nil &&
			(first.AreaPostingMode == "normal" || staff && first.AreaPostingMode == "read_only") &&
			(first.TopicState == "open" || staff && (first.TopicState == "locked" || first.TopicState == "hidden"))
		replyAction := ""
		replyPreview := ""
		cancelURL := ""
		token := csrfTokenFromContext(request.Context())
		if mayReply && len(token) == sessionCookieEncodedBytes {
			replyAction, viewErr = builder.Path("topics", identifier, "replies")
			if viewErr == nil {
				replyPreview, viewErr = builder.Path("topics", identifier, "replies", "preview")
			}
			if viewErr == nil {
				cancelURL, viewErr = builder.PathWithQuery(segments, currentQuery)
			}
		}
		moderationLinks, _ := request.Context().Value(userModerationLinksContextKey{}).(bool)
		activeStaffLinks := moderationLinks && staff && authentication.Access.Authenticated &&
			!authentication.Access.Suspended && authentication.Access.MutedUntil == nil
		posts := make([]topicPostItem, 0, len(loaded.Rows))
		if loaded.TotalPosts > 0 {
			for _, row := range loaded.Rows {
				validRow := sameTopicPostPresentationMetadata(first, row) && row.TotalVisiblePosts == loaded.TotalPosts &&
					row.PostID.Valid && row.PostID.Int64 > 0 && row.PostNumber.Valid && row.PostNumber.Int32 > 0 &&
					row.ThreadDepth >= 1 && row.ThreadDepth <= forum.MaximumReplyDepth && row.IsTombstone.Valid &&
					row.NodeOrdinal.Valid && row.NodeOrdinal.Int64 > 0
				if !validRow {
					invalid = true
					break
				}
				anchor := "post-" + strconv.FormatInt(row.PostID.Int64, 10)
				permalink, permalinkErr := builder.PathWithQueryAndFragment(segments, currentQuery, anchor)
				if permalinkErr != nil {
					viewErr = permalinkErr
					break
				}
				item := topicPostItem{
					Anchor: anchor, Permalink: permalink, Number: row.PostNumber.Int32,
					IndentClass: threadIndentClass(row.ThreadDepth), Tombstone: row.IsTombstone.Bool,
				}
				if row.ParentPostID.Valid {
					parentPage := 1 + (row.ParentNodeOrdinal.Int64-1)/int64(store.PostPageSize)
					parentQuery := url.Values(nil)
					if parentPage > 1 {
						parentQuery = url.Values{"page": {strconv.FormatInt(parentPage, 10)}}
					}
					item.ParentURL, viewErr = builder.PathWithQueryAndFragment(segments, parentQuery, "post-"+strconv.FormatInt(row.ParentPostID.Int64, 10))
					if row.ParentAuthorDisplayName.Valid {
						item.ParentLabel = "In reply to " + row.ParentAuthorDisplayName.String + " (post #" + strconv.FormatInt(int64(row.ParentPostNumber.Int32), 10) + ")"
					} else {
						item.ParentLabel = "In reply to deleted post #" + strconv.FormatInt(int64(row.ParentPostNumber.Int32), 10)
					}
				}
				if row.IsTombstone.Bool {
					posts = append(posts, item)
					continue
				}
				if !row.RenderedHtml.Valid || !row.RendererVersion.Valid || row.RendererVersion.String == "" || !row.Revision.Valid || row.Revision.Int32 <= 0 ||
					!row.PostCreatedAt.Valid || !row.PostUpdatedAt.Valid || !row.PostAuthorID.Valid || row.PostAuthorID.Int64 <= 0 ||
					!row.PostAuthorDisplayName.Valid || row.PostAuthorDisplayName.String == "" {
					invalid = true
					break
				}
				item.Author = row.PostAuthorDisplayName.String
				item.Created = row.PostCreatedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST")
				if row.PostEditedAt.Valid {
					item.Edited = "Edited " + row.PostEditedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST")
				}
				item.Body = contentrender.SanitizeHTML(row.RenderedHtml.String)
				if replyAction != "" {
					item.ReplyForm = publishingFormView{Heading: "Reply to post #" + strconv.FormatInt(int64(row.PostNumber.Int32), 10), ActionURL: replyAction, PreviewURL: replyPreview, CancelURL: cancelURL, CSRFToken: token, ParentPostID: strconv.FormatInt(row.PostID.Int64, 10), Reply: true}
					item.ShowReply = true
				}
				posts = append(posts, item)
				if activeStaffLinks && row.PostAuthorID.Int64 != authentication.Access.UserID {
					statusURL, statusErr := builder.Path("moderation", "users", strconv.FormatInt(row.PostAuthorID.Int64, 10))
					if statusErr != nil {
						viewErr = statusErr
						break
					}
					posts[len(posts)-1].AuthorStatusURL = statusURL
				}
				if authentication.Access.Authenticated && !authentication.Access.Suspended && authentication.Access.MutedUntil == nil &&
					row.PostAuthorID.Int64 == authentication.Access.UserID {
					if int64(row.Revision.Int32) < maximumEditFormRevision {
						editURL, editErr := builder.Path("posts", strconv.FormatInt(row.PostID.Int64, 10), "edit")
						if editErr != nil {
							viewErr = editErr
							break
						}
						posts[len(posts)-1].EditURL = editURL
					}
					token := csrfTokenFromContext(request.Context())
					if len(token) == sessionCookieEncodedBytes {
						deleteURL, deleteErr := builder.Path("posts", strconv.FormatInt(row.PostID.Int64, 10), "delete")
						if deleteErr != nil {
							viewErr = deleteErr
							break
						}
						posts[len(posts)-1].DeleteURL = deleteURL
						posts[len(posts)-1].CSRFToken = token
						posts[len(posts)-1].Revision = strconv.FormatInt(int64(row.Revision.Int32), 10)
					}
				}
			}
		}
		if invalid || viewErr != nil {
			serveError(response, request, http.StatusServiceUnavailable, unavailableView, "Topic unavailable", "This topic is temporarily unavailable.")
			return
		}
		var moderationControls []topicModerationView
		if staff && authentication.Access.Authenticated && !authentication.Access.Suspended && authentication.Access.MutedUntil == nil {
			token := csrfTokenFromContext(request.Context())
			actions := [][2]string(nil)
			switch first.TopicState {
			case "open":
				actions = [][2]string{{"lock", "Lock topic"}, {"hide", "Hide topic"}}
			case "locked":
				actions = [][2]string{{"unlock", "Unlock topic"}}
			case "hidden":
				actions = [][2]string{{"restore", "Restore topic"}}
			}
			if len(actions) > 0 && len(token) == sessionCookieEncodedBytes {
				moderationControls = make([]topicModerationView, 0, len(actions))
				for _, action := range actions {
					actionURL, actionErr := builder.Path("topics", identifier, action[0])
					if actionErr != nil {
						serveError(response, request, http.StatusServiceUnavailable, unavailableView, "Topic unavailable", "This topic is temporarily unavailable.")
						return
					}
					moderationControls = append(moderationControls, topicModerationView{ActionURL: actionURL, CSRFToken: token, SubmitLabel: action[1]})
				}
			}
		}
		replyForm := publishingFormView{}
		if replyAction != "" {
			for _, post := range posts {
				if post.Number == 1 && !post.Tombstone {
					replyForm = publishingFormView{
						Heading: "Reply", ActionURL: replyAction, PreviewURL: replyPreview, CancelURL: cancelURL,
						CSRFToken: token, ParentPostID: strconv.FormatInt(first.TopicFirstPostID, 10), Reply: true,
					}
					break
				}
			}
		}
		presentation := topicPostPageView{
			AreaName: first.AreaName, AreaURL: areaURL, Title: first.TopicTitle,
			StateLabel: stateLabel, Pinned: first.TopicPinnedAt.Valid,
			Author: first.TopicAuthorDisplayName, Started: first.TopicCreatedAt.Time.UTC().Format("Jan 2, 2006 15:04 MST"),
			Posts: posts, Number: pageNumber, TotalPosts: loaded.TotalPosts,
			PreviousURL: previousURL, NextURL: nextURL, ReplyForm: replyForm, ShowReply: replyForm.ActionURL != "",
			Moderation: moderationControls,
		}
		if renderErr := renderResponse(
			response, request, http.StatusOK,
			topicPostListPage(view, presentation),
			topicPostListContent(view, presentation),
		); renderErr != nil {
			panic(renderErr)
		}
	}), nil
}

// threadIndentClass maps logical ancestry to a closed, responsive visual
// indentation set. Logical ancestry remains intact beyond the six-level cap.
func threadIndentClass(depth int32) string {
	switch depth {
	case 1:
		return ""
	case 2:
		return "thread-depth-2"
	case 3:
		return "thread-depth-3"
	case 4:
		return "thread-depth-4"
	case 5:
		return "thread-depth-5"
	case 6:
		return "thread-depth-6"
	default:
		return "thread-depth-capped"
	}
}

// sameTopicPostPresentationMetadata proves every projected row belongs to the
// same access-filtered topic and breadcrumb authority as the first row.
//
// Complexity: time and auxiliary space are tight Theta(1).
func sameTopicPostPresentationMetadata(first, row db.GetVisibleTopicPostPageRow) bool {
	return row.AreaID == first.AreaID && row.AreaSlug == first.AreaSlug && row.AreaName == first.AreaName &&
		row.AreaDescription == first.AreaDescription && row.AreaPostingMode == first.AreaPostingMode &&
		row.TopicID == first.TopicID && row.TopicFirstPostID == first.TopicFirstPostID && row.TopicTitle == first.TopicTitle &&
		row.TopicState == first.TopicState && row.TopicPinnedAt == first.TopicPinnedAt && row.TopicCreatedAt == first.TopicCreatedAt &&
		row.TopicAuthorDisplayName == first.TopicAuthorDisplayName
}
