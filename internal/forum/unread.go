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
