package forum

import (
	"context"
	"errors"
	"fmt"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// FirstUnreadTarget is the validated navigation result for one authorized
// topic. A zero PostID means that no unread eligible post exists. Direct is
// true only when the target lies beyond the bounded topic-page window.
type FirstUnreadTarget struct {
	PostID int64
	Page   int32
	Direct bool
}

// LoadVisibleTopicPostPage adds one authenticated topic's private read state
// to the existing access-filtered topic page in the same read-only snapshot.
// Visitors retain the existing non-personalized query and nil ReadState.
func LoadVisibleTopicPostPage(ctx context.Context, beginner interface {
	db.DBTX
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}, actor policy.AccessContext, topicID int64, pageNumber int32) (store.VisibleTopicPostPage, error) {
	if ctx == nil {
		return store.VisibleTopicPostPage{}, fmt.Errorf("topic page context is required")
	}
	if beginner == nil {
		return store.VisibleTopicPostPage{}, fmt.Errorf("topic page transaction beginner is required")
	}
	if !actor.Valid() {
		return store.VisibleTopicPostPage{}, fmt.Errorf("topic page actor is invalid")
	}
	if !actor.Authenticated {
		return store.GetVisibleTopicPostPage(ctx, db.New(beginner), topicID, pageNumber, actor)
	}
	var page store.VisibleTopicPostPage
	err := store.WithinTxOptions(ctx, beginner, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(queries *db.Queries) error {
		if err := queries.ConfigureUnreadReadTransaction(ctx); err != nil {
			return fmt.Errorf("configure unread read transaction: %w", err)
		}
		var err error
		page, err = store.GetVisibleTopicPostPage(ctx, queries, topicID, pageNumber, actor)
		if err != nil {
			return err
		}
		row, err := queries.GetAuthorizedTopicReadState(ctx, db.GetAuthorizedTopicReadStateParams{
			ActorUserID: actor.UserID,
			TopicID:     topicID,
			IsStaff:     actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
			GroupIds:    actor.GroupIDs,
		})
		if err != nil {
			return fmt.Errorf("query authorized topic read state: %w", err)
		}
		state, valid := validatedTopicReadState(row.TopicID, row.NextPostNumber, row.ReadHead, row.LastReadPostNumber, row.ReadAt, row.ReadState)
		if !valid || row.TopicID != topicID {
			return fmt.Errorf("authorized topic read state is malformed")
		}
		page.ReadState = &state
		return nil
	})
	if err != nil {
		return store.VisibleTopicPostPage{}, fmt.Errorf("load topic page transaction: %w", err)
	}
	return page, nil
}

// FirstUnread resolves one authorized actor's lowest eligible unread post and
// its bounded tree-order placement in one read-only repeatable-read snapshot.
func FirstUnread(ctx context.Context, beginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}, actor policy.AccessContext, topicID int64) (FirstUnreadTarget, error) {
	if ctx == nil {
		return FirstUnreadTarget{}, fmt.Errorf("first unread context is required")
	}
	if beginner == nil {
		return FirstUnreadTarget{}, fmt.Errorf("first unread transaction beginner is required")
	}
	if !actor.Valid() || !actor.Authenticated || actor.Suspended {
		return FirstUnreadTarget{}, fmt.Errorf("first unread actor is invalid")
	}
	if topicID <= 0 {
		return FirstUnreadTarget{}, fmt.Errorf("first unread topic is invalid")
	}
	if err := ctx.Err(); err != nil {
		return FirstUnreadTarget{}, fmt.Errorf("first unread: %w", err)
	}
	var target FirstUnreadTarget
	err := store.WithinTxOptions(ctx, beginner, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(queries *db.Queries) error {
		if err := queries.ConfigureUnreadReadTransaction(ctx); err != nil {
			return fmt.Errorf("configure unread read transaction: %w", err)
		}
		row, err := queries.GetFirstUnreadTarget(ctx, db.GetFirstUnreadTargetParams{
			TopicID: topicID, IsStaff: actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
			GroupIds: actor.GroupIDs, ActorUserID: actor.UserID,
		})
		if err != nil {
			return fmt.Errorf("query first unread target: %w", err)
		}
		state, valid := validatedTopicReadState(row.TopicID, row.NextPostNumber, row.ReadHead, row.LastReadPostNumber, row.ReadAt, "")
		_ = state
		marker := int32(0)
		if row.LastReadPostNumber.Valid {
			marker = row.LastReadPostNumber.Int32
		}
		targetPresent := row.TargetPostID.Valid && row.TargetPostNumber.Valid
		targetAbsent := !row.TargetPostID.Valid && !row.TargetPostNumber.Valid && !row.TargetNodeOrdinal.Valid
		if !valid || row.TopicID != topicID || (!targetPresent && !targetAbsent) {
			return fmt.Errorf("first unread target is malformed")
		}
		if targetAbsent {
			if row.ReadHead > marker {
				return fmt.Errorf("first unread target is inconsistent")
			}
			return nil
		}
		if row.TargetPostID.Int64 <= 0 || row.TargetPostNumber.Int32 <= marker || row.TargetPostNumber.Int32 > row.ReadHead ||
			row.TargetPostNumber.Int32 >= row.NextPostNumber || row.TargetNodeOrdinal.Valid &&
			(row.TargetNodeOrdinal.Int64 <= 0 || row.TargetNodeOrdinal.Int64 > 250001) {
			return fmt.Errorf("first unread target is malformed")
		}
		target.PostID = row.TargetPostID.Int64
		if !row.TargetNodeOrdinal.Valid || row.TargetNodeOrdinal.Int64 > int64(store.PostPageSize)*int64(store.MaximumPostPage) {
			target.Direct = true
			return nil
		}
		target.Page = int32(1 + (row.TargetNodeOrdinal.Int64-1)/int64(store.PostPageSize))
		return nil
	})
	if err != nil {
		return FirstUnreadTarget{}, fmt.Errorf("first unread transaction: %w", err)
	}
	return target, nil
}

func validatedTopicReadState(topicID int64, nextPostNumber, readHead int32, marker pgtype.Int4, readAt pgtype.Timestamptz, encoded string) (store.ReadState, bool) {
	markerPresent := marker.Valid && readAt.Valid
	markerAbsent := !marker.Valid && !readAt.Valid
	if topicID <= 0 || nextPostNumber < 2 || readHead < 0 || readHead >= nextPostNumber ||
		(!markerPresent && !markerAbsent) || markerPresent && (marker.Int32 <= 0 || marker.Int32 >= nextPostNumber || readAt.InfinityModifier != pgtype.Finite) {
		return "", false
	}
	state := store.ReadStateRead
	if readHead > 0 && markerAbsent {
		state = store.ReadStateNew
	} else if readHead > 0 && marker.Int32 < readHead {
		state = store.ReadStateUnread
	}
	return state, encoded == "" || encoded == string(state)
}

// MarkTopicRead advances one authenticated actor's marker through the greatest
// currently visible post by another author. PostgreSQL chooses the boundary;
// the caller supplies no watermark. Equal, lower, stale, and concurrent
// attempts leave an existing marker and read_at unchanged. The operation never
// retries an unknown commit outcome.
//
// Complexity: for g actor groups and delegated authorization, indexed head,
// upsert, and marker-validation work D(g), time is O(g+D(g)), Omega(1), with no
// tighter bound because database scheduling is external. Auxiliary space is
// O(g+A(D)), Omega(1); the actor's group slice is passed without a copy. There
// is one transaction, three application statements, and at most one marker
// row. The first statement installs transaction-local statement and lock
// deadlines for the remaining work.
func MarkTopicRead(ctx context.Context, beginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}, actor policy.AccessContext, topicID int64) error {
	if ctx == nil {
		return fmt.Errorf("mark topic read context is required")
	}
	if beginner == nil {
		return fmt.Errorf("mark topic read transaction beginner is required")
	}
	if !actor.Valid() || !actor.Authenticated || actor.Suspended {
		return fmt.Errorf("mark topic read actor is invalid")
	}
	if topicID <= 0 {
		return fmt.Errorf("mark topic read topic is invalid")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("mark topic read: %w", err)
	}

	err := store.WithinTxOptions(ctx, beginner, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(queries *db.Queries) error {
		if err := queries.ConfigureMarkTopicReadTransaction(ctx); err != nil {
			return fmt.Errorf("configure mark-read transaction: %w", err)
		}
		boundary, err := queries.MarkTopicReadBoundary(ctx, db.MarkTopicReadBoundaryParams{
			TopicID: topicID, IsStaff: actor.Role == policy.RoleModerator || actor.Role == policy.RoleAdministrator,
			GroupIds: actor.GroupIDs, ActorUserID: actor.UserID,
		})
		if err != nil {
			return fmt.Errorf("select authorized mark-read boundary: %w", err)
		}
		if boundary.TopicID != topicID || boundary.NextPostNumber < 2 || boundary.SelectedPostNumber < 0 ||
			boundary.SelectedPostNumber >= boundary.NextPostNumber || boundary.SelectedPostNumber == 0 && boundary.Advanced {
			return fmt.Errorf("mark-read boundary returned an invalid result")
		}

		marker, err := queries.GetTopicReadMarker(ctx, db.GetTopicReadMarkerParams{ActorUserID: actor.UserID, TopicID: topicID})
		if errors.Is(err, pgx.ErrNoRows) && boundary.SelectedPostNumber == 0 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect mark-read marker: %w", err)
		}
		if marker.LastReadPostNumber <= 0 || marker.LastReadPostNumber >= boundary.NextPostNumber ||
			!marker.ReadAt.Valid || marker.ReadAt.InfinityModifier != pgtype.Finite ||
			boundary.SelectedPostNumber > 0 && marker.LastReadPostNumber < boundary.SelectedPostNumber ||
			boundary.Advanced && marker.LastReadPostNumber != boundary.SelectedPostNumber {
			return fmt.Errorf("mark-read marker returned an invalid result")
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("mark topic read transaction: %w", err)
	}
	return nil
}
