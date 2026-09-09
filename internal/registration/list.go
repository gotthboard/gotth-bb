package registration

import (
	"context"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	pendingPageSize       = 50
	pendingPageQueryLimit = 51
)

type Pending struct {
	ID, Revision                       int64
	DisplayName, VerifiedEmail, Status string
	ReconciliationClass                string
	IntakeAt                           time.Time
}

type PendingPage struct {
	Registrations []Pending
	NextAfter     int64
}

type pendingQuerier interface {
	ListPendingRegistrationsForAdministration(context.Context, db.ListPendingRegistrationsForAdministrationParams) ([]db.ListPendingRegistrationsForAdministrationRow, error)
}

// ListPending returns one bounded administrator queue without exposing either
// Authentik identity coordinate to the browser.
func ListPending(ctx context.Context, querier pendingQuerier, actor policy.AccessContext, observedAt time.Time, after int64) (PendingPage, error) {
	if ctx == nil || querier == nil || !policy.CanAdminister(actor) {
		return PendingPage{}, ErrDenied
	}
	if err := ctx.Err(); err != nil || observedAt.IsZero() || after < 0 {
		return PendingPage{}, ErrInput
	}
	rows, err := querier.ListPendingRegistrationsForAdministration(ctx, db.ListPendingRegistrationsForAdministrationParams{
		ActorUserID: actor.UserID, ObservedAt: pgtype.Timestamptz{Time: observedAt.UTC().Truncate(time.Microsecond), Valid: true},
		AfterRegistrationID: after, PageLimit: pendingPageQueryLimit,
	})
	if err != nil {
		return PendingPage{}, fmt.Errorf("%w: list pending registrations", ErrUnavailable)
	}
	if len(rows) == 0 {
		return PendingPage{}, ErrDenied
	}
	if len(rows) > pendingPageQueryLimit {
		return PendingPage{}, fmt.Errorf("%w: oversized pending page", ErrUnavailable)
	}
	page := PendingPage{Registrations: make([]Pending, 0, min(len(rows), pendingPageSize))}
	previous := after
	for index, row := range rows {
		if !row.RegistrationPresent {
			if len(rows) != 1 || index != 0 || row.ID != 0 || row.DisplayName != "" || row.VerifiedEmail != "" || row.Status != "" || row.AdministrationRevision != 0 || row.IntakeAt.Valid || row.ReconciliationClass.Valid {
				return PendingPage{}, fmt.Errorf("%w: malformed empty pending page", ErrUnavailable)
			}
			return page, nil
		}
		item := Pending{ID: row.ID, DisplayName: row.DisplayName, VerifiedEmail: row.VerifiedEmail, Status: row.Status, Revision: row.AdministrationRevision}
		if row.IntakeAt.Valid {
			item.IntakeAt = row.IntakeAt.Time.UTC().Truncate(time.Microsecond)
		}
		if row.ReconciliationClass.Valid {
			item.ReconciliationClass = row.ReconciliationClass.String
		}
		if !validPending(item) || item.ID <= previous {
			return PendingPage{}, fmt.Errorf("%w: malformed pending page", ErrUnavailable)
		}
		previous = item.ID
		if index == pendingPageSize {
			page.NextAfter = page.Registrations[len(page.Registrations)-1].ID
			continue
		}
		page.Registrations = append(page.Registrations, item)
	}
	return page, nil
}

func validPending(item Pending) bool {
	status := item.Status == "pending" || item.Status == "approval_required" || item.Status == "rejection_required"
	reconciliation := item.ReconciliationClass == "" || validIntakeText(item.ReconciliationClass, 1, 128, 128)
	return item.ID > 0 && item.Revision > 0 && !item.IntakeAt.IsZero() && item.IntakeAt.Year() >= 1 && item.IntakeAt.Year() <= 9999 &&
		validIntakeText(item.DisplayName, 1, 320, 80) && validIntakeText(item.VerifiedEmail, 3, 320, 320) && intakeAddressOnly(item.VerifiedEmail) && status && reconciliation
}
