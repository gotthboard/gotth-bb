package registration

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	expiryClaimLifetime = time.Minute
	expiryRetryBackoff  = time.Minute
)

type ExpiryReconciliationResult struct {
	Claimed, Completed, Failed int
}

type claimedExpiryIdentity struct {
	db.ClaimExpiredIdentityReconciliationsRow
	requestID pgtype.UUID
}

func ReconcileExpiredSuspensions(ctx context.Context, beginner transactionBeginner, remote ControlGateway, clock func() time.Time, random io.Reader) (ExpiryReconciliationResult, error) {
	if ctx == nil || beginner == nil || remote == nil || clock == nil || random == nil {
		return ExpiryReconciliationResult{}, ErrUnavailable
	}
	now, err := observedAt(clock)
	if err != nil {
		return ExpiryReconciliationResult{}, err
	}
	claims := make([]claimedExpiryIdentity, 0, 5)
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		rows, claimErr := queries.ClaimExpiredIdentityReconciliations(txctx, db.ClaimExpiredIdentityReconciliationsParams{ObservedAt: finiteTime(now), ClaimUntil: finiteTime(now.Add(expiryClaimLifetime))})
		if claimErr != nil || len(rows) > 5 {
			return fmt.Errorf("%w: claim expired identities", ErrUnavailable)
		}
		for _, row := range rows {
			if row.UserID <= 0 || row.AdministrationRevision <= 1 || (row.PreviousSyncState != "suspended" && row.PreviousSyncState != "removal_required" && row.PreviousSyncState != "grant_required") {
				return fmt.Errorf("%w: malformed expired identity", ErrUnavailable)
			}
			if _, parseErr := parseCanonicalUUID(row.Subject); parseErr != nil {
				return fmt.Errorf("%w: malformed expired identity", ErrUnavailable)
			}
			requestID, requestErr := randomRequestID(random)
			if requestErr != nil {
				return fmt.Errorf("%w: generate reconciliation request", ErrUnavailable)
			}
			auditID, auditErr := queries.RecordExpiredIdentityReconciliationRequest(txctx, db.RecordExpiredIdentityReconciliationRequestParams{
				UserID: pgtype.Int8{Int64: row.UserID, Valid: true}, Reason: pgtype.Text{String: "Finite suspension expired", Valid: true},
				PreviousSyncState: row.PreviousSyncState, PreviousRevision: row.AdministrationRevision - 1, AdministrationRevision: row.AdministrationRevision,
				RequestID: requestID, ObservedAt: finiteTime(now),
			})
			if auditErr != nil || auditID <= 0 {
				return fmt.Errorf("%w: audit expired identity claim", ErrUnavailable)
			}
			claims = append(claims, claimedExpiryIdentity{ClaimExpiredIdentityReconciliationsRow: row, requestID: requestID})
		}
		return nil
	})
	if err != nil {
		return ExpiryReconciliationResult{}, err
	}
	result := ExpiryReconciliationResult{Claimed: len(claims)}
	for _, claim := range claims {
		remoteErr := applyIdentityGrant(ctx, remote, claim.Subject)
		completedAt, clockErr := observedAt(clock)
		if clockErr != nil {
			return result, clockErr
		}
		if remoteErr == nil {
			err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
				row, completeErr := queries.CompleteExpiredIdentityReconciliation(txctx, db.CompleteExpiredIdentityReconciliationParams{
					ObservedAt: finiteTime(completedAt), UserID: claim.UserID, ExpectedRevision: claim.AdministrationRevision,
					Reason: pgtype.Text{String: "Finite suspension expired", Valid: true}, RequestID: claim.requestID,
				})
				if completeErr != nil || row.AdministrationRevision != claim.AdministrationRevision+1 || row.AuditID <= 0 {
					return fmt.Errorf("%w: complete expired identity", ErrUnavailable)
				}
				return nil
			})
			if err != nil {
				return result, err
			}
			result.Completed++
			continue
		}
		failure := remoteFailureClass(remoteErr)
		err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
			row, recordErr := queries.RecordExpiredIdentityReconciliationFailure(txctx, db.RecordExpiredIdentityReconciliationFailureParams{
				ObservedAt: finiteTime(completedAt), NextAttemptAt: finiteTime(completedAt.Add(expiryRetryBackoff)),
				FailureClass: pgtype.Text{String: failure, Valid: true}, UserID: claim.UserID, ExpectedRevision: claim.AdministrationRevision,
				Reason: pgtype.Text{String: "Finite suspension expired", Valid: true}, RequestID: claim.requestID,
			})
			if recordErr != nil || row.AdministrationRevision != claim.AdministrationRevision+1 || row.AuditID <= 0 {
				return fmt.Errorf("%w: record expired identity failure", ErrUnavailable)
			}
			return nil
		})
		if err != nil {
			return result, err
		}
		result.Failed++
	}
	return result, nil
}

func randomRequestID(source io.Reader) (pgtype.UUID, error) {
	var raw [16]byte
	if _, err := io.ReadFull(source, raw[:]); err != nil || raw == ([16]byte{}) {
		return pgtype.UUID{}, fmt.Errorf("request ID generation failed")
	}
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return pgtype.UUID{Bytes: raw, Valid: true}, nil
}
