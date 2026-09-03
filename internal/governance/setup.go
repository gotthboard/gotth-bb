package governance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/store"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type InitialAdministratorSetupStatus struct {
	Open     bool
	Eligible bool
}

type InitialAdministratorClaimResult struct {
	UserID           int64
	AuditID          int64
	RevokedSessionID int64
}

func LoadInitialAdministratorSetup(
	ctx context.Context,
	queries *db.Queries,
	clock func() time.Time,
	userID int64,
	issuer string,
	subject string,
) (InitialAdministratorSetupStatus, error) {
	if ctx == nil {
		return InitialAdministratorSetupStatus{}, fmt.Errorf("administrator setup context is required")
	}
	if queries == nil {
		return InitialAdministratorSetupStatus{}, fmt.Errorf("administrator setup queries are required")
	}
	if clock == nil {
		return InitialAdministratorSetupStatus{}, fmt.Errorf("administrator setup clock is required")
	}
	if userID < 0 || !validBootstrapText(issuer, 2048) || !validBootstrapText(subject, 512) {
		return InitialAdministratorSetupStatus{}, fmt.Errorf("administrator setup input is invalid")
	}
	if err := ctx.Err(); err != nil {
		return InitialAdministratorSetupStatus{}, fmt.Errorf("load administrator setup: %w", err)
	}
	now := clock()
	if now.IsZero() {
		return InitialAdministratorSetupStatus{}, fmt.Errorf("administrator setup clock returned a zero time")
	}
	atTime := pgtype.Timestamptz{Time: now.UTC().Truncate(time.Microsecond), Valid: true}
	bootstraps, err := queries.CountAdministratorBootstraps(ctx)
	if err != nil {
		return InitialAdministratorSetupStatus{}, fmt.Errorf("count administrator bootstraps: %w", err)
	}
	administrators, err := queries.CountActiveAdministrators(ctx, atTime)
	if err != nil {
		return InitialAdministratorSetupStatus{}, fmt.Errorf("count active administrators: %w", err)
	}
	if bootstraps != 0 || administrators != 0 {
		return InitialAdministratorSetupStatus{}, nil
	}
	status := InitialAdministratorSetupStatus{Open: true}
	if userID == 0 {
		return status, nil
	}
	user, err := queries.GetUserByExternalIdentity(ctx, db.GetUserByExternalIdentityParams{Issuer: issuer, Subject: subject})
	if errors.Is(err, pgx.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return InitialAdministratorSetupStatus{}, fmt.Errorf("load administrator setup identity: %w", err)
	}
	status.Eligible = user.ID == userID && user.ID > 0
	return status, nil
}

func ClaimInitialAdministrator(
	ctx context.Context,
	beginner transactionBeginner,
	clock func() time.Time,
	userID int64,
	sessionID int64,
	issuer string,
	subject string,
	requestID pgtype.UUID,
) (InitialAdministratorClaimResult, error) {
	if ctx == nil {
		return InitialAdministratorClaimResult{}, fmt.Errorf("administrator claim context is required")
	}
	if beginner == nil {
		return InitialAdministratorClaimResult{}, fmt.Errorf("administrator claim transaction beginner is required")
	}
	if clock == nil {
		return InitialAdministratorClaimResult{}, fmt.Errorf("administrator claim clock is required")
	}
	if userID <= 0 || sessionID <= 0 || !validBootstrapText(issuer, 2048) || !validBootstrapText(subject, 512) {
		return InitialAdministratorClaimResult{}, fmt.Errorf("administrator claim input is invalid")
	}
	if !requestID.Valid || requestID.Bytes == ([16]byte{}) {
		return InitialAdministratorClaimResult{}, fmt.Errorf("administrator claim request ID is invalid")
	}
	if err := ctx.Err(); err != nil {
		return InitialAdministratorClaimResult{}, fmt.Errorf("claim administrator setup: %w", err)
	}
	now := clock()
	if now.IsZero() {
		return InitialAdministratorClaimResult{}, fmt.Errorf("administrator claim clock returned a zero time")
	}
	atTime := pgtype.Timestamptz{Time: now.UTC().Truncate(time.Microsecond), Valid: true}
	result := InitialAdministratorClaimResult{}
	err := store.WithinTx(ctx, beginner, func(queries *db.Queries) error {
		locked, err := queries.LockGovernanceState(ctx)
		if err != nil {
			return fmt.Errorf("lock administrator governance state: %w", err)
		}
		if !locked {
			return fmt.Errorf("administrator governance singleton is missing")
		}
		bootstraps, err := queries.CountAdministratorBootstraps(ctx)
		if err != nil {
			return fmt.Errorf("count administrator bootstraps: %w", err)
		}
		if bootstraps != 0 {
			return ErrAdministratorSetupClosed
		}
		administrators, err := queries.CountActiveAdministrators(ctx, atTime)
		if err != nil {
			return fmt.Errorf("count active administrators: %w", err)
		}
		if administrators != 0 {
			return ErrAdministratorSetupClosed
		}
		claimed, err := queries.ClaimInitialAdministratorAndAudit(ctx, db.ClaimInitialAdministratorAndAuditParams{
			SessionID: sessionID,
			UserID:    userID,
			AtTime:    atTime,
			Issuer:    issuer,
			Subject:   subject,
			RequestID: requestID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrAdministratorSetupDenied
		}
		if err != nil {
			return fmt.Errorf("write administrator claim and audit: %w", err)
		}
		if claimed.UserID != userID || claimed.AuditID <= 0 || claimed.RevokedSessionID != sessionID {
			return fmt.Errorf("administrator claim returned an invalid result")
		}
		result = InitialAdministratorClaimResult{
			UserID:           claimed.UserID,
			AuditID:          claimed.AuditID,
			RevokedSessionID: claimed.RevokedSessionID,
		}
		return nil
	})
	if err != nil {
		return InitialAdministratorClaimResult{}, fmt.Errorf("claim first administrator: %w", err)
	}
	return result, nil
}
