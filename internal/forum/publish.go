package forum

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotthboard/gotth-bb/internal/abuse"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/render"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrInvalidPublishingInput = errors.New("invalid forum publishing input")
	ErrPublishingDenied       = errors.New("forum publishing denied")
	ErrPublicationRateLimited = errors.New("forum publication rate limited")
)

const MaximumReplyDepth int32 = 32

// InvalidPublishingInput identifies one safe form field without retaining or
// exposing submitted bytes.
type InvalidPublishingInput struct {
	Field string
}

// Error returns one bounded field-only diagnostic.
//
// Complexity: time and returned space are tight Theta(1); Field is one of the
// fixed service-owned names.
func (invalid InvalidPublishingInput) Error() string {
	return "invalid forum publishing " + invalid.Field
}

// Unwrap exposes only the stable validation class for errors.Is.
//
// Complexity: time and auxiliary space are tight Theta(1).
func (invalid InvalidPublishingInput) Unwrap() error {
	return ErrInvalidPublishingInput
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type PublishResult struct {
	TopicID     int64
	PostID      int64
	PostNumber  int32
	NodeOrdinal int64
}

// PublicationRateLimitError exposes only one bounded whole-second retry.
type PublicationRateLimitError struct {
	RetryAfterSeconds int64
}

func (limited PublicationRateLimitError) Error() string { return ErrPublicationRateLimited.Error() }
func (limited PublicationRateLimitError) Unwrap() error { return ErrPublicationRateLimited }

// RenderTopicDraft applies the exact bounded field validation and sanitized
// Markdown renderer used before topic publication. The opaque result may be
// presented as a preview or persisted only through its guarded methods.
//
// Complexity: for bounded field bytes n and Markdown bytes m, time is
// O(n+m+R(m)), Omega(1), and auxiliary/returned space is O(m+R(m)), Omega(1),
// where R is the renderer's documented work. There is no I/O or retained
// mutable state.
func RenderTopicDraft(areaSlug, title, markdownSource string) (render.RenderedMarkdown, error) {
	if err := validateTopicDraftFields(areaSlug, title); err != nil {
		return render.RenderedMarkdown{}, err
	}
	return renderPublishingDraft(markdownSource)
}

// validateTopicDraftFields applies the non-body topic validation shared by
// preview and publication while retaining publication's cancellation boundary
// before Markdown rendering.
//
// Complexity: for bounded slug/title bytes n, time is O(n), Omega(1), and
// auxiliary space is tight Theta(1).
func validateTopicDraftFields(areaSlug, title string) error {
	if !policy.ValidAreaSlug(areaSlug) {
		return InvalidPublishingInput{Field: "area"}
	}
	if !validTopicTitle(title) {
		return InvalidPublishingInput{Field: "title"}
	}
	return nil
}

// RenderReplyDraft applies the exact bounded sanitized Markdown renderer used
// before reply publication.
//
// Complexity: for bounded Markdown bytes m, time is O(m+R(m)), Omega(1), and
// auxiliary/returned space is O(m+R(m)), Omega(1), where R is the renderer's
// documented work. There is no I/O or retained mutable state.
func RenderReplyDraft(markdownSource string) (render.RenderedMarkdown, error) {
	return renderPublishingDraft(markdownSource)
}

// renderPublishingDraft owns the one shared renderer-to-validation-error
// mapping used by preview and both publication paths.
//
// Complexity: for bounded Markdown bytes m, time is O(m+R(m)), Omega(1), and
// auxiliary/returned space is O(m+R(m)), Omega(1), where R is the renderer's
// documented work.
func renderPublishingDraft(markdownSource string) (render.RenderedMarkdown, error) {
	rendered, err := render.RenderMarkdown(markdownSource)
	if err != nil {
		return render.RenderedMarkdown{}, InvalidPublishingInput{Field: "markdown"}
	}
	return rendered, nil
}

// CreateTopic validates and renders one first post before locking and
// revalidating the current account, its groups, and the target area. It commits
// the durable publication window, topic, first post, counters, and database
// timestamps as one transaction.
//
// Complexity: for title bytes t, bounded Markdown bytes m, actor groups a,
// area groups p, renderer work R(m), and database work D, time is
// O(t+m+a*p+a+p+R(m)+D), Omega(1), without one tight bound because invalid
// input and external database work vary. Auxiliary space is O(m+R(m)+p),
// Omega(1); the driver and renderer own their result buffers. There is one
// transaction with eight application statements plus begin/commit, no retry,
// and no detached work.
func CreateTopic(
	ctx context.Context,
	beginner transactionBeginner,
	publicationPolicy abuse.PublicationPolicy,
	actor policy.AccessContext,
	areaSlug string,
	title string,
	markdownSource string,
) (PublishResult, error) {
	if ctx == nil {
		return PublishResult{}, fmt.Errorf("create topic context is required")
	}
	if beginner == nil {
		return PublishResult{}, fmt.Errorf("create topic transaction beginner is required")
	}
	if !publicationPolicy.Valid() {
		return PublishResult{}, fmt.Errorf("create topic publication policy is invalid")
	}
	if !actor.Valid() || !actor.Authenticated {
		return PublishResult{}, fmt.Errorf("create topic actor is invalid")
	}
	if err := validateTopicDraftFields(areaSlug, title); err != nil {
		return PublishResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return PublishResult{}, fmt.Errorf("create topic: %w", err)
	}
	rendered, err := renderPublishingDraft(markdownSource)
	if err != nil {
		return PublishResult{}, err
	}
	renderedHTML, rendererVersion, err := rendered.PersistenceValues()
	if err != nil {
		return PublishResult{}, fmt.Errorf("create topic body persistence: %w", err)
	}
	postSearchText, searchProjectionVersion, err := rendered.SearchProjectionValues()
	if err != nil {
		return PublishResult{}, fmt.Errorf("create topic search projection: %w", err)
	}

	result := PublishResult{}
	err = store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		if err := queries.ConfigurePublicationTransaction(ctx); err != nil {
			return fmt.Errorf("configure topic publication transaction: %w", err)
		}
		currentActor, lockedActor, err := lockPublicationActor(ctx, queries, actor)
		if err != nil {
			return fmt.Errorf("lock topic publication actor: %w", err)
		}
		area, err := queries.LockAreaForTopicCreation(ctx, areaSlug)
		if err != nil {
			return fmt.Errorf("lock topic area: %w", err)
		}
		if area.ID <= 0 {
			return fmt.Errorf("topic area lock returned an invalid result")
		}
		areaPolicy, err := lockedAreaPolicy(ctx, queries, area.ID, area.Visibility, area.PostingMode)
		if err != nil {
			return fmt.Errorf("load topic area policy: %w", err)
		}
		if !policy.CanCreateTopic(currentActor, areaPolicy) {
			return ErrPublishingDenied
		}
		atTime, err := admitPublication(ctx, queries, publicationPolicy, lockedActor)
		if err != nil {
			return err
		}
		created, err := queries.CreateTopicAndFirstPost(ctx, db.CreateTopicAndFirstPostParams{
			AreaID: area.ID, AuthorID: currentActor.UserID, Title: title, AtTime: atTime,
			TopicSearchText: norm.NFC.String(title), SearchProjectionVersion: pgtype.Text{String: searchProjectionVersion, Valid: true},
			MarkdownSource: markdownSource, RenderedHtml: renderedHTML, RendererVersion: rendererVersion, PostSearchText: postSearchText,
		})
		if err != nil {
			return fmt.Errorf("insert topic and first post: %w", err)
		}
		if created.TopicID <= 0 || created.PostID <= 0 || created.PostNumber != 1 || created.NodeOrdinal != 1 {
			return fmt.Errorf("topic creation returned an invalid result")
		}
		result = PublishResult{TopicID: created.TopicID, PostID: created.PostID, PostNumber: created.PostNumber, NodeOrdinal: created.NodeOrdinal}
		return nil
	})
	if err != nil {
		return PublishResult{}, fmt.Errorf("create topic transaction: %w", err)
	}
	return result, nil
}

// CreateReply validates and renders one reply before locking and revalidating
// the current account, its groups, and the target topic/area policy. The
// durable publication window, immutable post-number allocation, insertion,
// and topic advancement commit as one transaction.
//
// Complexity: for bounded Markdown bytes m, actor groups a, area groups p,
// renderer work R(m), and database work D, time is
// O(m+a*p+a+p+R(m)+D), Omega(1), without one tight bound because invalid input
// and external database work vary. Auxiliary space is O(m+R(m)+p), Omega(1).
// There is one transaction with eight application statements plus
// begin/commit, no retry, and no detached work; the topic row lock serializes
// reply-number allocation.
func CreateReply(
	ctx context.Context,
	beginner transactionBeginner,
	publicationPolicy abuse.PublicationPolicy,
	actor policy.AccessContext,
	topicID int64,
	parentPostID int64,
	markdownSource string,
) (PublishResult, error) {
	if ctx == nil {
		return PublishResult{}, fmt.Errorf("create reply context is required")
	}
	if beginner == nil {
		return PublishResult{}, fmt.Errorf("create reply transaction beginner is required")
	}
	if !publicationPolicy.Valid() {
		return PublishResult{}, fmt.Errorf("create reply publication policy is invalid")
	}
	if !actor.Valid() || !actor.Authenticated {
		return PublishResult{}, fmt.Errorf("create reply actor is invalid")
	}
	if topicID <= 0 {
		return PublishResult{}, InvalidPublishingInput{Field: "topic"}
	}
	if parentPostID <= 0 {
		return PublishResult{}, InvalidPublishingInput{Field: "parent"}
	}
	if err := ctx.Err(); err != nil {
		return PublishResult{}, fmt.Errorf("create reply: %w", err)
	}
	rendered, err := renderPublishingDraft(markdownSource)
	if err != nil {
		return PublishResult{}, err
	}
	renderedHTML, rendererVersion, err := rendered.PersistenceValues()
	if err != nil {
		return PublishResult{}, fmt.Errorf("create reply body persistence: %w", err)
	}
	postSearchText, searchProjectionVersion, err := rendered.SearchProjectionValues()
	if err != nil {
		return PublishResult{}, fmt.Errorf("create reply search projection: %w", err)
	}

	result := PublishResult{}
	err = store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		if err := queries.ConfigurePublicationTransaction(ctx); err != nil {
			return fmt.Errorf("configure reply publication transaction: %w", err)
		}
		currentActor, lockedActor, err := lockPublicationActor(ctx, queries, actor)
		if err != nil {
			return fmt.Errorf("lock reply publication actor: %w", err)
		}
		topic, err := queries.LockTopicForReply(ctx, db.LockTopicForReplyParams{TopicID: topicID, ParentPostID: parentPostID})
		if err != nil {
			return fmt.Errorf("lock reply topic: %w", err)
		}
		if topic.TopicID != topicID || topic.AreaID <= 0 || topic.ParentPostID != parentPostID || topic.ParentDepth < 1 || topic.ParentDepth >= MaximumReplyDepth {
			return fmt.Errorf("reply topic lock returned an invalid result")
		}
		areaPolicy, err := lockedAreaPolicy(ctx, queries, topic.AreaID, topic.Visibility, topic.PostingMode)
		if err != nil {
			return fmt.Errorf("load reply area policy: %w", err)
		}
		if !policy.CanReply(currentActor, areaPolicy, policy.TopicState(topic.TopicState)) {
			return ErrPublishingDenied
		}
		atTime, err := admitPublication(ctx, queries, publicationPolicy, lockedActor)
		if err != nil {
			return err
		}
		created, err := queries.CreateReplyAndAdvanceTopic(ctx, db.CreateReplyAndAdvanceTopicParams{
			AuthorID: currentActor.UserID, MarkdownSource: markdownSource, RenderedHtml: renderedHTML,
			RendererVersion: rendererVersion, ParentPostID: pgtype.Int8{Int64: parentPostID, Valid: true}, AtTime: atTime,
			PostSearchText: postSearchText, SearchProjectionVersion: pgtype.Text{String: searchProjectionVersion, Valid: true}, TopicID: topicID,
		})
		if err != nil {
			return fmt.Errorf("insert reply and advance topic: %w", err)
		}
		if created.TopicID != topicID || created.PostID <= 0 || created.PostNumber < 2 || created.NodeOrdinal < 2 {
			return fmt.Errorf("reply creation returned an invalid result")
		}
		result = PublishResult{TopicID: created.TopicID, PostID: created.PostID, PostNumber: created.PostNumber, NodeOrdinal: created.NodeOrdinal}
		return nil
	})
	if err != nil {
		return PublishResult{}, fmt.Errorf("create reply transaction: %w", err)
	}
	return result, nil
}

func lockPublicationActor(ctx context.Context, queries *db.Queries, actor policy.AccessContext) (policy.AccessContext, db.LockPublicationActorRow, error) {
	locked, err := queries.LockPublicationActor(ctx, actor.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return policy.AccessContext{}, db.LockPublicationActorRow{}, ErrPublishingDenied
		}
		return policy.AccessContext{}, db.LockPublicationActorRow{}, err
	}
	if locked.ID != actor.UserID || locked.ID <= 0 || !finitePublishingTime(locked.CreatedAt) || !finitePublishingTime(locked.ObservedAt) ||
		locked.PublicationCount < 0 || locked.PublicationCount > 100_000 ||
		locked.PublicationWindowStartedAt.Valid != (locked.PublicationCount > 0) ||
		locked.PublicationWindowStartedAt.Valid && (!finitePublishingTime(locked.PublicationWindowStartedAt) || locked.PublicationWindowStartedAt.Time.Before(locked.CreatedAt.Time)) {
		return policy.AccessContext{}, db.LockPublicationActorRow{}, ErrPublishingDenied
	}
	role, valid := publishingRole(locked.Role)
	if !valid || role != actor.Role {
		return policy.AccessContext{}, db.LockPublicationActorRow{}, ErrPublishingDenied
	}
	if locked.SuspendedAt.Valid && !finitePublishingTime(locked.SuspendedAt) ||
		locked.SuspendedUntil.Valid && !finitePublishingTime(locked.SuspendedUntil) ||
		locked.MutedUntil.Valid && !finitePublishingTime(locked.MutedUntil) ||
		locked.SuspendedUntil.Valid && !locked.SuspendedAt.Valid ||
		locked.SuspendedAt.Valid && locked.SuspendedAt.Time.Before(locked.CreatedAt.Time) ||
		locked.SuspendedUntil.Valid && !locked.SuspendedUntil.Time.After(locked.SuspendedAt.Time) ||
		locked.MutedUntil.Valid && !locked.MutedUntil.Time.After(locked.CreatedAt.Time) ||
		locked.ObservedAt.Time.Before(locked.CreatedAt.Time) {
		return policy.AccessContext{}, db.LockPublicationActorRow{}, ErrPublishingDenied
	}
	observedAt := locked.ObservedAt.Time.UTC()
	suspended := locked.SuspendedAt.Valid && !locked.SuspendedAt.Time.After(observedAt) &&
		(!locked.SuspendedUntil.Valid || locked.SuspendedUntil.Time.After(observedAt))
	muted := locked.MutedUntil.Valid && locked.MutedUntil.Time.After(observedAt)
	groups, err := queries.ListLockedPublicationActorGroupIDs(ctx, actor.UserID)
	if err != nil {
		return policy.AccessContext{}, db.LockPublicationActorRow{}, err
	}
	for index, groupID := range groups {
		if groupID <= 0 || index > 0 && groupID <= groups[index-1] {
			return policy.AccessContext{}, db.LockPublicationActorRow{}, ErrPublishingDenied
		}
	}
	current := policy.AccessContext{Authenticated: true, UserID: locked.ID, Role: role, GroupIDs: groups, Suspended: suspended}
	if muted {
		mutedUntil := locked.MutedUntil.Time.UTC()
		current.MutedUntil = &mutedUntil
	}
	if !current.Valid() {
		return policy.AccessContext{}, db.LockPublicationActorRow{}, ErrPublishingDenied
	}
	return current, locked, nil
}

func admitPublication(ctx context.Context, queries *db.Queries, publicationPolicy abuse.PublicationPolicy, locked db.LockPublicationActorRow) (pgtype.Timestamptz, error) {
	databaseNow, err := queries.PublicationDatabaseTime(ctx)
	if err != nil {
		return pgtype.Timestamptz{}, fmt.Errorf("read publication database time: %w", err)
	}
	if !finitePublishingTime(databaseNow) {
		return pgtype.Timestamptz{}, fmt.Errorf("publication database time is invalid")
	}
	var startedAt *time.Time
	if locked.PublicationWindowStartedAt.Valid {
		value := locked.PublicationWindowStartedAt.Time.UTC()
		startedAt = &value
	}
	decision, err := publicationPolicy.DecidePublication(locked.CreatedAt.Time, databaseNow.Time, startedAt, locked.PublicationCount)
	if err != nil {
		return pgtype.Timestamptz{}, fmt.Errorf("decide publication admission: %w", err)
	}
	if retry := decision.RetryAfterSeconds(); retry > 0 {
		return pgtype.Timestamptz{}, PublicationRateLimitError{RetryAfterSeconds: retry}
	}
	replaced, err := queries.ReplacePublicationWindow(ctx, db.ReplacePublicationWindowParams{
		WindowStartedAt:  pgtype.Timestamptz{Time: decision.StartedAt, Valid: true},
		PublicationCount: decision.Count,
		ActorUserID:      locked.ID,
	})
	if err != nil {
		return pgtype.Timestamptz{}, fmt.Errorf("replace publication window: %w", err)
	}
	if !finitePublishingTime(replaced.PublicationWindowStartedAt) || !replaced.PublicationWindowStartedAt.Time.Equal(decision.StartedAt) || replaced.PublicationCount != decision.Count {
		return pgtype.Timestamptz{}, fmt.Errorf("publication window update returned an invalid result")
	}
	return pgtype.Timestamptz{Time: databaseNow.Time.UTC().Truncate(time.Microsecond), Valid: true}, nil
}

func finitePublishingTime(value pgtype.Timestamptz) bool {
	return value.Valid && value.InfinityModifier == pgtype.Finite && !value.Time.IsZero()
}

func publishingRole(value string) (policy.Role, bool) {
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

// publishingTime returns one finite, UTC, PostgreSQL-microsecond timestamp for
// edit and delete operations, which do not spend the publication window.
func publishingTime(clock func() time.Time) (pgtype.Timestamptz, error) {
	now := clock()
	if now.IsZero() {
		return pgtype.Timestamptz{}, fmt.Errorf("publishing clock returned a zero time")
	}
	return pgtype.Timestamptz{Time: now.UTC().Truncate(time.Microsecond), Valid: true}, nil
}

// lockedAreaPolicy obtains and validates the group mappings protected by the
// caller's already-held area lock.
//
// Complexity: for p group mappings and delegated query work Q(p), time and
// returned space are O(p+Q(p)), Omega(1), without a tight bound because driver
// work varies. It performs one database round trip and no copy beyond sqlc's
// result slice.
func lockedAreaPolicy(ctx context.Context, queries *db.Queries, areaID int64, visibility, postingMode string) (policy.AreaPolicy, error) {
	groupIDs, err := queries.LockAreaGroupIDs(ctx, areaID)
	if err != nil {
		return policy.AreaPolicy{}, fmt.Errorf("lock area group mappings: %w", err)
	}
	return policy.AreaPolicy{Visibility: policy.Visibility(visibility), PostingMode: policy.PostingMode(postingMode), GroupIDs: groupIDs}, nil
}

// validTopicTitle checks the database's 1-200-character bound and rejects
// blank or control-bearing presentation text without normalizing it.
//
// Complexity: for t title bytes, time is O(t), Omega(1), and tight Theta(t)
// for valid input. Auxiliary space is tight Theta(1); no normalized copy is
// produced.
func validTopicTitle(title string) bool {
	if !utf8.ValidString(title) || strings.TrimSpace(title) == "" {
		return false
	}
	runes := utf8.RuneCountInString(title)
	return runes <= 200 && strings.IndexFunc(title, unicode.IsControl) < 0
}
