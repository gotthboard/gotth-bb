package forum

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"git.dannyhunn.com/agents/gotth-bb/internal/policy"
	"git.dannyhunn.com/agents/gotth-bb/internal/render"
	"git.dannyhunn.com/agents/gotth-bb/internal/store"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var (
	ErrInvalidPublishingInput = errors.New("invalid forum publishing input")
	ErrPublishingDenied       = errors.New("forum publishing denied")
)

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
	TopicID    int64
	PostID     int64
	PostNumber int32
}

// CreateTopic validates and renders one first post before locking the current
// area policy. It authorizes against that locked policy and commits the topic,
// first post, counters, and timestamps as one transaction.
//
// Complexity: for title bytes t, bounded Markdown bytes m, actor groups a,
// area groups p, renderer work R(m), and database work D, time is
// O(t+m+a*p+a+p+R(m)+D), Omega(1), without one tight bound because invalid
// input and external database work vary. Auxiliary space is O(m+R(m)+p),
// Omega(1); the driver and renderer own their result buffers. There is one
// transaction with three application statements plus begin/commit, no retry,
// and no detached work.
func CreateTopic(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
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
	if clock == nil {
		return PublishResult{}, fmt.Errorf("create topic clock is required")
	}
	if !actor.Valid() || !actor.Authenticated {
		return PublishResult{}, fmt.Errorf("create topic actor is invalid")
	}
	if !policy.ValidAreaSlug(areaSlug) {
		return PublishResult{}, InvalidPublishingInput{Field: "area"}
	}
	if !validTopicTitle(title) {
		return PublishResult{}, InvalidPublishingInput{Field: "title"}
	}
	if err := ctx.Err(); err != nil {
		return PublishResult{}, fmt.Errorf("create topic: %w", err)
	}
	rendered, err := render.RenderMarkdown(markdownSource)
	if err != nil {
		return PublishResult{}, InvalidPublishingInput{Field: "markdown"}
	}
	renderedHTML, rendererVersion, err := rendered.PersistenceValues()
	if err != nil {
		return PublishResult{}, fmt.Errorf("create topic body persistence: %w", err)
	}
	atTime, err := publishingTime(clock)
	if err != nil {
		return PublishResult{}, fmt.Errorf("create topic: %w", err)
	}

	result := PublishResult{}
	err = store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
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
		if !policy.CanCreateTopic(actor, areaPolicy) {
			return ErrPublishingDenied
		}
		created, err := queries.CreateTopicAndFirstPost(ctx, db.CreateTopicAndFirstPostParams{
			AreaID: area.ID, AuthorID: actor.UserID, Title: title, AtTime: atTime,
			MarkdownSource: markdownSource, RenderedHtml: renderedHTML, RendererVersion: rendererVersion,
		})
		if err != nil {
			return fmt.Errorf("insert topic and first post: %w", err)
		}
		if created.TopicID <= 0 || created.PostID <= 0 || created.PostNumber != 1 {
			return fmt.Errorf("topic creation returned an invalid result")
		}
		result = PublishResult{TopicID: created.TopicID, PostID: created.PostID, PostNumber: created.PostNumber}
		return nil
	})
	if err != nil {
		return PublishResult{}, fmt.Errorf("create topic transaction: %w", err)
	}
	return result, nil
}

// CreateReply validates and renders one reply before locking its current topic
// and area policy. Authorization, immutable post-number allocation, insertion,
// and topic counter/activity advancement commit as one transaction.
//
// Complexity: for bounded Markdown bytes m, actor groups a, area groups p,
// renderer work R(m), and database work D, time is
// O(m+a*p+a+p+R(m)+D), Omega(1), without one tight bound because invalid input
// and external database work vary. Auxiliary space is O(m+R(m)+p), Omega(1).
// There is one transaction with three application statements plus
// begin/commit, no retry, and no detached work; the topic row lock serializes
// reply-number allocation.
func CreateReply(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	actor policy.AccessContext,
	topicID int64,
	markdownSource string,
) (PublishResult, error) {
	if ctx == nil {
		return PublishResult{}, fmt.Errorf("create reply context is required")
	}
	if beginner == nil {
		return PublishResult{}, fmt.Errorf("create reply transaction beginner is required")
	}
	if clock == nil {
		return PublishResult{}, fmt.Errorf("create reply clock is required")
	}
	if !actor.Valid() || !actor.Authenticated {
		return PublishResult{}, fmt.Errorf("create reply actor is invalid")
	}
	if topicID <= 0 {
		return PublishResult{}, InvalidPublishingInput{Field: "topic"}
	}
	if err := ctx.Err(); err != nil {
		return PublishResult{}, fmt.Errorf("create reply: %w", err)
	}
	rendered, err := render.RenderMarkdown(markdownSource)
	if err != nil {
		return PublishResult{}, InvalidPublishingInput{Field: "markdown"}
	}
	renderedHTML, rendererVersion, err := rendered.PersistenceValues()
	if err != nil {
		return PublishResult{}, fmt.Errorf("create reply body persistence: %w", err)
	}
	atTime, err := publishingTime(clock)
	if err != nil {
		return PublishResult{}, fmt.Errorf("create reply: %w", err)
	}

	result := PublishResult{}
	err = store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		topic, err := queries.LockTopicForReply(ctx, topicID)
		if err != nil {
			return fmt.Errorf("lock reply topic: %w", err)
		}
		if topic.TopicID != topicID || topic.AreaID <= 0 {
			return fmt.Errorf("reply topic lock returned an invalid result")
		}
		areaPolicy, err := lockedAreaPolicy(ctx, queries, topic.AreaID, topic.Visibility, topic.PostingMode)
		if err != nil {
			return fmt.Errorf("load reply area policy: %w", err)
		}
		if !policy.CanReply(actor, areaPolicy, policy.TopicState(topic.TopicState)) {
			return ErrPublishingDenied
		}
		created, err := queries.CreateReplyAndAdvanceTopic(ctx, db.CreateReplyAndAdvanceTopicParams{
			AuthorID: actor.UserID, MarkdownSource: markdownSource, RenderedHtml: renderedHTML,
			RendererVersion: rendererVersion, AtTime: atTime, TopicID: topicID,
		})
		if err != nil {
			return fmt.Errorf("insert reply and advance topic: %w", err)
		}
		if created.TopicID != topicID || created.PostID <= 0 || created.PostNumber < 2 {
			return fmt.Errorf("reply creation returned an invalid result")
		}
		result = PublishResult{TopicID: created.TopicID, PostID: created.PostID, PostNumber: created.PostNumber}
		return nil
	})
	if err != nil {
		return PublishResult{}, fmt.Errorf("create reply transaction: %w", err)
	}
	return result, nil
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

// publishingTime returns one finite, UTC, PostgreSQL-microsecond timestamp.
//
// Complexity: time and auxiliary space are tight Theta(1); the supplied clock
// is called exactly once.
func publishingTime(clock func() time.Time) (pgtype.Timestamptz, error) {
	now := clock()
	if now.IsZero() {
		return pgtype.Timestamptz{}, fmt.Errorf("publishing clock returned a zero time")
	}
	return pgtype.Timestamptz{Time: now.UTC().Truncate(time.Microsecond), Valid: true}, nil
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
