package db

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// StreamAdministrationAreaGroupIDs returns the target area's group IDs in
// canonical order. The caller must close and fully inspect the returned rows.
func (q *Queries) StreamAdministrationAreaGroupIDs(ctx context.Context, areaID int64) (pgx.Rows, error) {
	return q.db.Query(ctx, `
SELECT group_id
FROM public.area_groups
WHERE area_id = $1
ORDER BY group_id`, areaID)
}
