package registration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/gotthboard/gotth-bb/internal/authentikgateway"
	"github.com/gotthboard/gotth-bb/internal/policy"
	"github.com/gotthboard/gotth-bb/internal/store/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	adoptionHandleLifetime = 5 * time.Minute
	adoptionHandleRawBytes = 65
)

type ControlGateway interface {
	Gateway
	PendingUsers(context.Context) ([]authentikgateway.User, bool, error)
	User(context.Context, string) (authentikgateway.UserState, error)
}

type Orphan struct {
	DisplayName, VerifiedEmail, Handle string
}

type coordinateQuerier interface {
	MatchPendingRegistrationCoordinates(context.Context, db.MatchPendingRegistrationCoordinatesParams) ([]db.MatchPendingRegistrationCoordinatesRow, error)
}

type AdoptionInput struct {
	Handle, Reason string
	RequestID      pgtype.UUID
}

type AdoptionResult struct {
	RegistrationID, Revision, AuditID int64
	Inserted                          bool
}

// ListPendingAdministration appends only remote pending identities absent by
// both immutable coordinates. Remote failure or coordinate drift preserves the
// local queue and emits no adoption handle.
func ListPendingAdministration(ctx context.Context, querier interface {
	pendingQuerier
	coordinateQuerier
}, remote ControlGateway, actor policy.AccessContext, observedAt time.Time, after int64, key [32]byte) (PendingPage, error) {
	page, err := ListPending(ctx, querier, actor, observedAt, after)
	if err != nil {
		return PendingPage{}, err
	}
	if remote == nil || key == ([32]byte{}) {
		return PendingPage{}, ErrUnavailable
	}
	users, more, remoteErr := remote.PendingUsers(ctx)
	if remoteErr != nil {
		page.RemoteUnavailable = true
		return page, nil
	}
	if len(users) > 51 {
		page.RemoteUnavailable = true
		return page, nil
	}
	page.RemoteMore = more
	if len(users) == 0 {
		return page, nil
	}
	ids := make([]int64, len(users))
	subjects := make([]string, len(users))
	seenIDs := make(map[int64]struct{}, len(users))
	seenSubjects := make(map[string]struct{}, len(users))
	for index, user := range users {
		if _, err := pendingIdentity(user); err != nil {
			page.RemoteUnavailable = true
			return page, nil
		}
		if _, duplicate := seenIDs[user.ID]; duplicate {
			page.RemoteUnavailable = true
			return page, nil
		}
		if _, duplicate := seenSubjects[user.UUID]; duplicate {
			page.RemoteUnavailable = true
			return page, nil
		}
		seenIDs[user.ID], seenSubjects[user.UUID] = struct{}{}, struct{}{}
		ids[index], subjects[index] = user.ID, user.UUID
	}
	rows, queryErr := querier.MatchPendingRegistrationCoordinates(ctx, db.MatchPendingRegistrationCoordinatesParams{
		ActorUserID: actor.UserID, ObservedAt: finiteTime(observedAt.UTC().Truncate(time.Microsecond)),
		AuthentikUserIds: ids, AuthentikSubjects: subjects,
	})
	if queryErr != nil || len(rows) == 0 || len(rows) > 103 || !rows[0].ActorPresent {
		return PendingPage{}, fmt.Errorf("%w: match pending identities", ErrUnavailable)
	}
	matchedIDs := make(map[int64]string, len(rows))
	matchedSubjects := make(map[string]int64, len(rows))
	for _, row := range rows {
		if !row.ActorPresent {
			return PendingPage{}, ErrDenied
		}
		if !row.RegistrationPresent {
			if len(rows) != 1 || row.ID != 0 || row.AuthentikUserID != 0 || row.AuthentikSubject != "" {
				return PendingPage{}, fmt.Errorf("%w: malformed coordinate result", ErrUnavailable)
			}
			continue
		}
		if row.ID <= 0 || row.AuthentikUserID <= 0 {
			return PendingPage{}, fmt.Errorf("%w: malformed coordinate result", ErrUnavailable)
		}
		if _, parseErr := parseCanonicalUUID(row.AuthentikSubject); parseErr != nil {
			return PendingPage{}, fmt.Errorf("%w: malformed coordinate result", ErrUnavailable)
		}
		matchedIDs[row.AuthentikUserID] = row.AuthentikSubject
		matchedSubjects[row.AuthentikSubject] = row.AuthentikUserID
	}
	page.Orphans = make([]Orphan, 0, len(users))
	for _, user := range users {
		byID, idMatch := matchedIDs[user.ID]
		bySubject, subjectMatch := matchedSubjects[user.UUID]
		if idMatch || subjectMatch {
			if !idMatch || !subjectMatch || byID != user.UUID || bySubject != user.ID {
				page.Orphans = nil
				page.RemoteUnavailable = true
				return page, nil
			}
			continue
		}
		displayName, _ := pendingIdentity(user)
		handle, handleErr := issueAdoptionHandle(observedAt, key, user.ID, user.UUID)
		if handleErr != nil {
			return PendingPage{}, fmt.Errorf("%w: issue adoption handle", ErrUnavailable)
		}
		page.Orphans = append(page.Orphans, Orphan{DisplayName: displayName, VerifiedEmail: user.Email, Handle: handle})
	}
	return page, nil
}

// Adopt revalidates administrator authority before remote I/O, re-fetches the
// signed identity and its three memberships, then inserts and audits the local
// pending row in a second short transaction. It never grants remote access.
func Adopt(ctx context.Context, beginner transactionBeginner, remote ControlGateway, clock func() time.Time, actor policy.AccessContext, input AdoptionInput, key [32]byte) (AdoptionResult, error) {
	if ctx == nil || beginner == nil || remote == nil || clock == nil || !policy.CanAdminister(actor) {
		return AdoptionResult{}, ErrDenied
	}
	if !input.RequestID.Valid || input.RequestID.Bytes == ([16]byte{}) || !validReason(input.Reason) || key == ([32]byte{}) {
		return AdoptionResult{}, ErrInput
	}
	now, err := observedAt(clock)
	if err != nil {
		return AdoptionResult{}, err
	}
	userID, subject, err := verifyAdoptionHandle(input.Handle, now, key)
	if err != nil {
		return AdoptionResult{}, ErrInput
	}
	if err := inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		return lockAdministrator(txctx, queries, actor.UserID, now)
	}); err != nil {
		return AdoptionResult{}, fmt.Errorf("authorize pending adoption: %w", err)
	}
	state, err := remote.User(ctx, subject)
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("%w: re-fetch pending identity", ErrRemote)
	}
	displayName, err := pendingIdentity(state.User)
	if err != nil || state.ID != userID || !state.Active || !state.Pending || state.Accepted || state.Suspended {
		return AdoptionResult{}, fmt.Errorf("%w: pending identity changed", ErrConflict)
	}
	subjectUUID, _ := parseCanonicalUUID(subject)
	var result AdoptionResult
	err = inDecisionTx(ctx, beginner, func(txctx context.Context, queries *db.Queries) error {
		if err := lockAdministrator(txctx, queries, actor.UserID, now); err != nil {
			return err
		}
		row, insertErr := queries.InsertOrLoadPendingRegistrationAdoption(txctx, db.InsertOrLoadPendingRegistrationAdoptionParams{
			AuthentikUserID: userID, AuthentikSubject: subjectUUID, DisplayName: displayName,
			VerifiedEmail: state.Email, IntakeAt: finiteTime(now),
		})
		if errors.Is(insertErr, pgx.ErrNoRows) {
			return ErrConflict
		}
		if insertErr != nil || row.ID <= 0 || row.AdministrationRevision <= 0 {
			return fmt.Errorf("%w: persist pending adoption", ErrUnavailable)
		}
		result = AdoptionResult{RegistrationID: row.ID, Revision: row.AdministrationRevision, Inserted: row.Inserted}
		if !row.Inserted {
			return nil
		}
		auditID, auditErr := queries.RecordPendingRegistrationAdoption(txctx, db.RecordPendingRegistrationAdoptionParams{
			ActorUserID: pgtype.Int8{Int64: actor.UserID, Valid: true}, Reason: pgtype.Text{String: input.Reason, Valid: true},
			RegistrationRef: registrationReference(key, row.ID), AdministrationRevision: row.AdministrationRevision,
			RequestID: input.RequestID, ObservedAt: finiteTime(now),
		})
		if auditErr != nil || auditID <= 0 {
			return fmt.Errorf("%w: audit pending adoption", ErrUnavailable)
		}
		result.AuditID = auditID
		return nil
	})
	if err != nil {
		return AdoptionResult{}, fmt.Errorf("adopt pending registration: %w", err)
	}
	return result, nil
}

func pendingIdentity(user authentikgateway.User) (string, error) {
	displayName := user.Name
	if displayName == "" {
		displayName = user.Username
	}
	if user.ID <= 0 || !user.Active || !validIntakeText(user.Username, 1, 150, 150) || !validIntakeText(displayName, 1, 320, 80) || !validIntakeText(user.Email, 3, 320, 320) || !intakeAddressOnly(user.Email) {
		return "", ErrRemote
	}
	if _, err := parseCanonicalUUID(user.UUID); err != nil {
		return "", ErrRemote
	}
	return displayName, nil
}

func issueAdoptionHandle(now time.Time, key [32]byte, userID int64, subject string) (string, error) {
	parsed, err := parseCanonicalUUID(subject)
	if err != nil || key == ([32]byte{}) || userID <= 0 || now.IsZero() {
		return "", ErrInput
	}
	var raw [adoptionHandleRawBytes]byte
	raw[0] = 1
	binary.BigEndian.PutUint64(raw[1:9], uint64(now.UTC().Truncate(time.Second).Add(adoptionHandleLifetime).Unix()))
	binary.BigEndian.PutUint64(raw[9:17], uint64(userID))
	copy(raw[17:33], parsed.Bytes[:])
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("gotth-bb/pending-adoption-handle/v1\x00"))
	_, _ = mac.Write(raw[:33])
	copy(raw[33:], mac.Sum(nil))
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func verifyAdoptionHandle(encoded string, now time.Time, key [32]byte) (int64, string, error) {
	if len(encoded) != base64.RawURLEncoding.EncodedLen(adoptionHandleRawBytes) || key == ([32]byte{}) || now.IsZero() {
		return 0, "", ErrInput
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) != adoptionHandleRawBytes || base64.RawURLEncoding.EncodeToString(raw) != encoded || raw[0] != 1 {
		return 0, "", ErrInput
	}
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("gotth-bb/pending-adoption-handle/v1\x00"))
	_, _ = mac.Write(raw[:33])
	if !hmac.Equal(raw[33:], mac.Sum(nil)) {
		return 0, "", ErrInput
	}
	expires := int64(binary.BigEndian.Uint64(raw[1:9]))
	userID := int64(binary.BigEndian.Uint64(raw[9:17]))
	if expires <= 0 || userID <= 0 || now.UTC().Unix() > expires || time.Unix(expires, 0).After(now.UTC().Add(adoptionHandleLifetime)) {
		return 0, "", ErrInput
	}
	var subjectBytes [16]byte
	copy(subjectBytes[:], raw[17:33])
	subject, err := canonicalUUID(pgtype.UUID{Bytes: subjectBytes, Valid: true})
	if err != nil {
		return 0, "", ErrInput
	}
	if _, err := parseCanonicalUUID(subject); err != nil {
		return 0, "", ErrInput
	}
	return userID, subject, nil
}
