//go:build integration

package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const initialSessionTestDatabase = "gotth_bb_alpha1_auth_session_test"

func TestCreateInitialSessionOnPostgreSQL17(t *testing.T) {
	databaseURL := os.Getenv("GOTTH_BB_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("GOTTH_BB_TEST_DATABASE_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	adminConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("pgx.ParseConfig() returned error: %v", err)
	}
	adminConfig.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatalf("connect PostgreSQL admin database: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+initialSessionTestDatabase+" WITH (FORCE)"); err != nil {
		t.Fatalf("drop stale auth session test database: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+initialSessionTestDatabase); err != nil {
		t.Fatalf("create auth session test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = admin.Exec(cleanupContext, "DROP DATABASE IF EXISTS "+initialSessionTestDatabase+" WITH (FORCE)")
	})
	testConfig := adminConfig.Copy()
	testConfig.Database = initialSessionTestDatabase
	if err := migration.Apply(ctx, testConfig, migrations.Files()); err != nil {
		t.Fatalf("migration.Apply() returned error: %v", err)
	}
	connections := make([]*pgx.Conn, 2)
	for index := range connections {
		connections[index], err = pgx.ConnectConfig(ctx, testConfig)
		if err != nil {
			t.Fatalf("connect auth session test database %d: %v", index, err)
		}
		connection := connections[index]
		t.Cleanup(func() { _ = connection.Close(context.Background()) })
	}

	email := "member@example.test"
	avatar := "https://auth.example.test/avatar.png"
	claims := verifiedIdentityClaims{
		issuer: "https://auth.example.test/application/o/forum/", subject: "subject-1",
		displayName: "First Member", email: &email, avatarURL: &avatar,
	}
	firstTime := time.Date(2026, time.September, 1, 16, 40, 0, 123456789, time.UTC)
	firstRaw := bytes.Repeat([]byte{0x11}, sessionTokenBytes)
	first, err := createInitialSession(ctx, connections[0], bytes.NewReader(firstRaw), func() time.Time { return firstTime }, 24*time.Hour, claims)
	if err != nil {
		t.Fatalf("createInitialSession() first login: %v", err)
	}
	if first.userID == 0 || first.sessionID == 0 || first.token != base64.RawURLEncoding.EncodeToString(firstRaw) ||
		!first.expiresAt.Equal(firstTime.UTC().Truncate(time.Microsecond).Add(24*time.Hour)) {
		t.Fatalf("first session = %+v", first)
	}
	firstIssuedAt := firstTime.UTC().Truncate(time.Microsecond)
	firstTokenHash := sha256.Sum256([]byte(first.token))
	var storedTokenHash []byte
	var storedRole string
	var storedIssuedAt, storedLastSeenAt, storedValidatedAt, storedExpiresAt time.Time
	if err := connections[0].QueryRow(ctx, `SELECT
		s.token_hash, u.role, s.issued_at, s.last_seen_at, s.validated_at, s.expires_at
		FROM public.sessions AS s
		JOIN public.users AS u ON u.id = s.user_id
		WHERE s.id = $1`, first.sessionID).Scan(
		&storedTokenHash, &storedRole, &storedIssuedAt, &storedLastSeenAt, &storedValidatedAt, &storedExpiresAt,
	); err != nil || !bytes.Equal(storedTokenHash, firstTokenHash[:]) || storedRole != "member" ||
		!storedIssuedAt.Equal(firstIssuedAt) || !storedLastSeenAt.Equal(firstIssuedAt) ||
		!storedValidatedAt.Equal(firstIssuedAt) || !storedExpiresAt.Equal(first.expiresAt) {
		t.Fatalf("stored first session = (hash %x, role %q, issued %s, seen %s, validated %s, expires %s, error %v)",
			storedTokenHash, storedRole, storedIssuedAt, storedLastSeenAt, storedValidatedAt, storedExpiresAt, err)
	}
	if _, err := connections[0].Exec(ctx, "UPDATE public.users SET role = 'moderator' WHERE id = $1", first.userID); err != nil {
		t.Fatalf("assign local moderator role: %v", err)
	}
	claims.displayName = "Refreshed Member"
	claims.email = nil
	claims.avatarURL = nil
	refreshTime := firstTime.Add(time.Minute)
	second, err := createInitialSession(ctx, connections[0], bytes.NewReader(bytes.Repeat([]byte{0x22}, sessionTokenBytes)), func() time.Time { return refreshTime }, 12*time.Hour, claims)
	if err != nil || second.userID != first.userID || second.sessionID == first.sessionID {
		t.Fatalf("createInitialSession() refresh = (%+v, %v)", second, err)
	}
	var displayName, role string
	var storedEmail, storedAvatar *string
	if err := connections[0].QueryRow(ctx, "SELECT display_name, email, avatar_url, role FROM public.users WHERE id = $1", first.userID).Scan(&displayName, &storedEmail, &storedAvatar, &role); err != nil ||
		displayName != "Refreshed Member" || storedEmail != nil || storedAvatar != nil || role != "moderator" {
		t.Fatalf("refreshed user = (%q, %v, %v, %q, %v)", displayName, storedEmail, storedAvatar, role, err)
	}
	var identityCreatedAt, identityVerifiedAt time.Time
	if err := connections[0].QueryRow(ctx, `SELECT created_at, last_verified_at
		FROM public.external_identities WHERE issuer = $1 AND subject = $2`, claims.issuer, claims.subject).Scan(
		&identityCreatedAt, &identityVerifiedAt,
	); err != nil || !identityCreatedAt.Equal(firstIssuedAt) ||
		!identityVerifiedAt.Equal(refreshTime.UTC().Truncate(time.Microsecond)) {
		t.Fatalf("refreshed identity timestamps = (created %s, verified %s, error %v)", identityCreatedAt, identityVerifiedAt, err)
	}

	concurrentClaims := verifiedIdentityClaims{
		issuer: claims.issuer, subject: "concurrent-subject", displayName: "Concurrent Member",
	}
	start := make(chan struct{})
	results := make(chan createdInitialSession, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for index, connection := range connections {
		index, connection := index, connection
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			created, createErr := createInitialSession(
				ctx, connection, bytes.NewReader(bytes.Repeat([]byte{byte(0x30 + index)}, sessionTokenBytes)),
				func() time.Time { return refreshTime.Add(time.Minute) }, 24*time.Hour, concurrentClaims,
			)
			results <- created
			errorsChannel <- createErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for createErr := range errorsChannel {
		if createErr != nil {
			t.Fatalf("concurrent createInitialSession() returned error: %v", createErr)
		}
	}
	var concurrentUserID int64
	seenSessionIDs := make(map[int64]bool)
	seenTokens := make(map[string]bool)
	for created := range results {
		if concurrentUserID == 0 {
			concurrentUserID = created.userID
		}
		if created.userID != concurrentUserID || created.sessionID == 0 || seenSessionIDs[created.sessionID] || created.token == "" || seenTokens[created.token] {
			t.Fatalf("concurrent session = %+v", created)
		}
		seenSessionIDs[created.sessionID] = true
		seenTokens[created.token] = true
	}
	var users, identities, sessions int
	if err := connections[0].QueryRow(ctx, `SELECT
		(SELECT count(*) FROM public.users),
		(SELECT count(*) FROM public.external_identities),
		(SELECT count(*) FROM public.sessions)`).Scan(&users, &identities, &sessions); err != nil || users != 2 || identities != 2 || sessions != 4 {
		t.Fatalf("post-concurrency counts = (%d users, %d identities, %d sessions, %v)", users, identities, sessions, err)
	}

	rotationAt := refreshTime.Add(90 * time.Second).UTC().Truncate(time.Microsecond)
	rotationEmail := "revalidated@example.test"
	rotationClaims := claims
	rotationClaims.email = &rotationEmail
	rotationRaw := bytes.Repeat([]byte{0x44}, sessionTokenBytes)
	rotated, err := rotateRevalidatedSession(
		ctx, connections[0], bytes.NewReader(rotationRaw), func() time.Time { return rotationAt },
		24*time.Hour, 30*time.Minute, second.sessionID, second.token, rotationClaims,
	)
	if err != nil || rotated.userID != first.userID || rotated.sessionID <= 0 || rotated.sessionID == second.sessionID ||
		rotated.token != base64.RawURLEncoding.EncodeToString(rotationRaw) || !rotated.expiresAt.Equal(rotationAt.Add(24*time.Hour)) {
		t.Fatalf("rotateRevalidatedSession() = (%+v, %v)", rotated, err)
	}
	rotationTokenHash := sha256.Sum256([]byte(rotated.token))
	var oldRevokedAt *time.Time
	var rotatedIssuedAt, rotatedLastSeenAt, rotatedValidatedAt, rotatedExpiresAt time.Time
	var rotatedRole, rotatedEmail string
	if err := connections[0].QueryRow(ctx, `SELECT old_session.revoked_at,
		new_session.issued_at, new_session.last_seen_at, new_session.validated_at, new_session.expires_at,
		forum_user.role, forum_user.email
		FROM public.sessions AS old_session
		JOIN public.sessions AS new_session ON new_session.user_id = old_session.user_id
		JOIN public.users AS forum_user ON forum_user.id = new_session.user_id
		WHERE old_session.id = $1 AND new_session.id = $2 AND new_session.token_hash = $3`,
		second.sessionID, rotated.sessionID, rotationTokenHash[:],
	).Scan(&oldRevokedAt, &rotatedIssuedAt, &rotatedLastSeenAt, &rotatedValidatedAt, &rotatedExpiresAt, &rotatedRole, &rotatedEmail); err != nil ||
		oldRevokedAt == nil || !oldRevokedAt.Equal(rotationAt) || !rotatedIssuedAt.Equal(rotationAt) ||
		!rotatedLastSeenAt.Equal(rotationAt) || !rotatedValidatedAt.Equal(rotationAt) ||
		!rotatedExpiresAt.Equal(rotated.expiresAt) || rotatedRole != "moderator" || rotatedEmail != rotationEmail {
		t.Fatalf("stored rotation = (revoked %v, issued %s, seen %s, validated %s, expires %s, role %q, email %q, error %v)",
			oldRevokedAt, rotatedIssuedAt, rotatedLastSeenAt, rotatedValidatedAt, rotatedExpiresAt, rotatedRole, rotatedEmail, err)
	}
	rotationQueries := db.New(connections[0])
	rotationObservedAt := pgtype.Timestamptz{Time: rotationAt, Valid: true}
	rotationIdleCutoff := pgtype.Timestamptz{Time: rotationAt.Add(-30 * time.Minute), Valid: true}
	secondTokenHash := sha256.Sum256([]byte(second.token))
	if got, err := rotationQueries.GetActiveSession(ctx, db.GetActiveSessionParams{
		TokenHash: secondTokenHash[:], ObservedAt: rotationObservedAt, IdleCutoff: rotationIdleCutoff,
	}); !errors.Is(err, pgx.ErrNoRows) || !reflect.DeepEqual(got, db.GetActiveSessionRow{}) {
		t.Fatalf("old rotated GetActiveSession() = (%+v, %v), want zero/no rows", got, err)
	}
	if got, err := rotationQueries.GetActiveSession(ctx, db.GetActiveSessionParams{
		TokenHash: rotationTokenHash[:], ObservedAt: rotationObservedAt, IdleCutoff: rotationIdleCutoff,
	}); err != nil || got.SessionID != rotated.sessionID || got.UserID != first.userID {
		t.Fatalf("replacement GetActiveSession() = (%+v, %v)", got, err)
	}

	mismatchClaims := claims
	mismatchClaims.subject = "different-subject"
	mismatchRaw := bytes.Repeat([]byte{0x45}, sessionTokenBytes)
	mismatch, err := rotateRevalidatedSession(
		ctx, connections[0], bytes.NewReader(mismatchRaw), func() time.Time { return rotationAt.Add(time.Minute) },
		24*time.Hour, 30*time.Minute, first.sessionID, first.token, mismatchClaims,
	)
	if err == nil || mismatch != (createdRevalidatedSession{}) {
		t.Fatalf("identity-mismatch rotation = (%+v, %v), want zero/error", mismatch, err)
	}
	mismatchTokenHash := sha256.Sum256([]byte(base64.RawURLEncoding.EncodeToString(mismatchRaw)))
	var firstStillActive bool
	var mismatchSessions int
	if err := connections[0].QueryRow(ctx, `SELECT
		(SELECT revoked_at IS NULL FROM public.sessions WHERE id = $1),
		(SELECT count(*) FROM public.sessions WHERE token_hash = $2)`,
		first.sessionID, mismatchTokenHash[:],
	).Scan(&firstStillActive, &mismatchSessions); err != nil || !firstStillActive || mismatchSessions != 0 {
		t.Fatalf("mismatch rollback = (old active %t, replacement count %d, %v)", firstStillActive, mismatchSessions, err)
	}

	claims.displayName = "Must Roll Back"
	failed, err := createInitialSession(ctx, connections[0], bytes.NewReader(firstRaw), func() time.Time { return refreshTime.Add(2 * time.Minute) }, 24*time.Hour, claims)
	if err == nil || failed != (createdInitialSession{}) {
		t.Fatalf("duplicate-token transaction = (%+v, %v), want zero/error", failed, err)
	}
	var rolledBackName string
	if err := connections[0].QueryRow(ctx, "SELECT display_name FROM public.users WHERE id = $1", first.userID).Scan(&rolledBackName); err != nil || rolledBackName != "Refreshed Member" {
		t.Fatalf("failed session transaction changed profile = (%q, %v)", rolledBackName, err)
	}

	precisionClaims := verifiedIdentityClaims{
		issuer: claims.issuer, subject: "precision-subject", displayName: "Precision Member",
	}
	precisionTime := refreshTime.Add(3 * time.Minute).UTC().Truncate(time.Microsecond)
	for index, maximumAge := range []time.Duration{time.Microsecond, time.Microsecond + time.Nanosecond} {
		created, err := createInitialSession(
			ctx, connections[0], bytes.NewReader(bytes.Repeat([]byte{byte(0x50 + index)}, sessionTokenBytes)),
			func() time.Time { return precisionTime }, maximumAge, precisionClaims,
		)
		wantExpiresAt := precisionTime.Add(time.Microsecond)
		if err != nil || !created.expiresAt.Equal(wantExpiresAt) {
			t.Fatalf("precision session %s = (%+v, %v), want expiry %s", maximumAge, created, err, wantExpiresAt)
		}
		var storedPrecisionExpiry time.Time
		if err := connections[0].QueryRow(ctx, "SELECT expires_at FROM public.sessions WHERE id = $1", created.sessionID).Scan(&storedPrecisionExpiry); err != nil ||
			!storedPrecisionExpiry.Equal(wantExpiresAt) {
			t.Fatalf("stored precision expiry %s = (%s, %v), want %s", maximumAge, storedPrecisionExpiry, err, wantExpiresAt)
		}
	}

	harness := newOIDCExchangeHarness(t)
	defer harness.server.Close()
	serviceTime := refreshTime.Add(5 * time.Minute).UTC().Truncate(time.Microsecond)
	serviceEntropy := bytes.NewReader(sequentialBytes(512))
	serviceReturnPath := "/bb/topics/42"
	service := &Service{
		provider: harness.discover(t), database: connections[0], queries: db.New(connections[0]),
		entropy: serviceEntropy, clock: func() time.Time { return serviceTime }, sessionMaximumAge: 24 * time.Hour,
		validateReturnPath: func(raw string) (string, error) {
			if raw != serviceReturnPath {
				return "", errors.New("unexpected return path")
			}
			return raw, nil
		},
	}
	serviceMaterial, err := beginInitialLogin(
		ctx, service.queries.InsertOIDCLoginAttempt, service.entropy, service.clock,
		service.validateReturnPath, serviceReturnPath,
	)
	if err != nil {
		t.Fatalf("beginInitialLogin() service attempt: %v", err)
	}
	harness.material = serviceMaterial
	serviceToken, returnedPath, serviceExpiresAt, err := service.CompleteInitialLogin(ctx, serviceMaterial.state, "service-success")
	decodedServiceToken, decodeErr := base64.RawURLEncoding.Strict().DecodeString(serviceToken)
	if err != nil || decodeErr != nil || len(decodedServiceToken) != sessionTokenBytes || returnedPath != serviceReturnPath ||
		!serviceExpiresAt.Equal(serviceTime.Add(24*time.Hour)) || harness.tokenRequestCount("service-success") != 1 {
		t.Fatalf("Service.CompleteInitialLogin() = (token bytes %d, path %q, expiry %s, requests %d, errors %v/%v)",
			len(decodedServiceToken), returnedPath, serviceExpiresAt, harness.tokenRequestCount("service-success"), err, decodeErr)
	}
	var serviceDisplayName, serviceRole string
	var serviceSessions int
	if err := connections[0].QueryRow(ctx, `SELECT u.display_name, u.role,
		(SELECT count(*) FROM public.sessions AS s WHERE s.user_id = u.id)
		FROM public.users AS u
		JOIN public.external_identities AS i ON i.user_id = u.id
		WHERE i.issuer = $1 AND i.subject = $2`, harness.issuer, "subject-1").Scan(
		&serviceDisplayName, &serviceRole, &serviceSessions,
	); err != nil || serviceDisplayName != "Danny Hunn" || serviceRole != "member" || serviceSessions != 1 {
		t.Fatalf("service-created identity = (%q, %q, %d sessions, %v)", serviceDisplayName, serviceRole, serviceSessions, err)
	}
	replayToken, replayPath, replayExpiry, err := service.CompleteInitialLogin(ctx, serviceMaterial.state, "service-success")
	if err == nil || replayToken != "" || replayPath != "" || !replayExpiry.IsZero() || harness.tokenRequestCount("service-success") != 1 {
		t.Fatalf("replayed Service.CompleteInitialLogin() = (%q, %q, %s, requests %d, %v)",
			replayToken, replayPath, replayExpiry, harness.tokenRequestCount("service-success"), err)
	}

	authorizationURL, browserState, err := service.BeginInitialLogin(ctx, serviceReturnPath)
	parsedAuthorizationURL, parseErr := url.Parse(authorizationURL)
	decodedBrowserState, decodeErr := base64.RawURLEncoding.Strict().DecodeString(browserState)
	if err != nil || parseErr != nil || decodeErr != nil || len(decodedBrowserState) != loginSecretBytes {
		t.Fatalf("Service.BeginInitialLogin() = (URL %q, state bytes %d, errors %v/%v/%v)",
			authorizationURL, len(decodedBrowserState), err, parseErr, decodeErr)
	}
	authorizationQuery := parsedAuthorizationURL.Query()
	exactAuthorizationParameters := len(authorizationQuery) == 8
	for _, values := range authorizationQuery {
		exactAuthorizationParameters = exactAuthorizationParameters && len(values) == 1
	}
	if parsedAuthorizationURL.Scheme != "http" || parsedAuthorizationURL.Host != harness.server.Listener.Addr().String() ||
		parsedAuthorizationURL.Path != "/authorize" || authorizationQuery.Get("state") != browserState ||
		authorizationQuery.Get("client_id") != "gotth-bb" || authorizationQuery.Get("response_type") != "code" ||
		authorizationQuery.Get("redirect_uri") != "https://forum.example/bb/auth/callback" ||
		authorizationQuery.Get("scope") != "openid profile email" || authorizationQuery.Get("nonce") == "" ||
		authorizationQuery.Get("code_challenge") == "" || authorizationQuery.Get("code_challenge_method") != "S256" || !exactAuthorizationParameters ||
		authorizationQuery.Has("code_verifier") || authorizationQuery.Has("client_secret") {
		t.Fatalf("Service.BeginInitialLogin() authorization URL = %q", authorizationURL)
	}
	stateHash := sha256.Sum256([]byte(browserState))
	var storedPurpose, storedReturnPath string
	var unconsumed bool
	if err := connections[0].QueryRow(ctx, `SELECT purpose, return_path, consumed_at IS NULL
		FROM public.oidc_login_attempts WHERE state_hash = $1`, stateHash[:]).Scan(
		&storedPurpose, &storedReturnPath, &unconsumed,
	); err != nil || storedPurpose != "login" || storedReturnPath != serviceReturnPath || !unconsumed {
		t.Fatalf("service-started login attempt = (%q, %q, unconsumed %t, %v)",
			storedPurpose, storedReturnPath, unconsumed, err)
	}

	serviceTokenHash := sha256.Sum256([]byte(serviceToken))
	authenticatedAt := serviceTime.Add(10 * time.Minute)
	authenticate := func(observedAt, idleCutoff time.Time) (db.GetActiveSessionRow, error) {
		return service.queries.GetActiveSession(ctx, db.GetActiveSessionParams{
			TokenHash:  serviceTokenHash[:],
			ObservedAt: pgtype.Timestamptz{Time: observedAt, Valid: true},
			IdleCutoff: pgtype.Timestamptz{Time: idleCutoff, Valid: true},
		})
	}
	activeSession, err := authenticate(authenticatedAt, authenticatedAt.Add(-30*time.Minute))
	if err != nil || activeSession.SessionID <= 0 || activeSession.UserID <= 0 ||
		activeSession.Role != "member" || activeSession.GroupIds == nil || len(activeSession.GroupIds) != 0 ||
		!activeSession.IssuedAt.Time.Equal(serviceTime) || !activeSession.LastSeenAt.Time.Equal(serviceTime) ||
		!activeSession.ValidatedAt.Time.Equal(serviceTime) || !activeSession.ExpiresAt.Time.Equal(serviceExpiresAt) {
		t.Fatalf("GetActiveSession() = (%+v, %v)", activeSession, err)
	}
	var firstGroupID, secondGroupID int64
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by)
VALUES ('Session authority first', $1) RETURNING id`, activeSession.UserID).Scan(&firstGroupID); err != nil {
		t.Fatalf("insert first session-authority group: %v", err)
	}
	if err := connections[0].QueryRow(ctx, `INSERT INTO public.forum_groups (name, created_by)
VALUES ('Session authority second', $1) RETURNING id`, activeSession.UserID).Scan(&secondGroupID); err != nil {
		t.Fatalf("insert second session-authority group: %v", err)
	}
	if firstGroupID >= secondGroupID {
		t.Fatalf("session-authority group IDs = (%d, %d), want insertion order", firstGroupID, secondGroupID)
	}
	if _, err := connections[0].Exec(ctx, `INSERT INTO public.forum_group_members (group_id, user_id, granted_by)
VALUES ($1, $3, $3), ($2, $3, $3)`, secondGroupID, firstGroupID, activeSession.UserID); err != nil {
		t.Fatalf("insert session-authority memberships: %v", err)
	}
	activeSession, err = authenticate(authenticatedAt, authenticatedAt.Add(-30*time.Minute))
	if err != nil || !reflect.DeepEqual(activeSession.GroupIds, []int64{firstGroupID, secondGroupID}) {
		t.Fatalf("GetActiveSession() group IDs = (%v, %v), want [%d %d]", activeSession.GroupIds, err, firstGroupID, secondGroupID)
	}
	touchParams := db.TouchSessionParams{
		ObservedAt: pgtype.Timestamptz{Time: authenticatedAt, Valid: true},
		SessionID:  activeSession.SessionID,
		TouchBefore: pgtype.Timestamptz{
			Time: authenticatedAt.Add(-5 * time.Minute), Valid: true,
		},
	}
	service.clock = func() time.Time { return authenticatedAt }
	service.sessionIdleTimeout = 30 * time.Minute
	service.revalidationInterval = 5 * time.Minute
	authentication, err := service.AuthenticateSession(ctx, serviceToken)
	var storedLastSeen time.Time
	if scanErr := connections[0].QueryRow(ctx, "SELECT last_seen_at FROM public.sessions WHERE id = $1", activeSession.SessionID).Scan(&storedLastSeen); err != nil || scanErr != nil ||
		!authentication.Access.Authenticated || authentication.Access.UserID != activeSession.UserID ||
		authentication.Access.Role != RoleMember || !reflect.DeepEqual(authentication.Access.GroupIDs, []int64{firstGroupID, secondGroupID}) ||
		authentication.Access.Suspended || authentication.Access.MutedUntil != nil ||
		!authentication.Access.ValidatedAt.Equal(serviceTime) || !authentication.RequiresRevalidation || !storedLastSeen.Equal(authenticatedAt) {
		t.Fatalf("authenticateSession() = (%+v, stored %s, errors %v/%v)", authentication, storedLastSeen, err, scanErr)
	}
	if touched, err := service.queries.TouchSession(ctx, touchParams); err != nil || touched != 0 {
		t.Fatalf("repeated TouchSession() = (%d, %v), want zero/nil", touched, err)
	}
	idleBoundaryAt := serviceTime.Add(40 * time.Minute)
	idleCutoff := idleBoundaryAt.Add(-30 * time.Minute)
	if _, err := connections[0].Exec(ctx, "UPDATE public.sessions SET last_seen_at = $1 WHERE id = $2", idleCutoff, activeSession.SessionID); err != nil {
		t.Fatalf("set exact idle boundary: %v", err)
	}
	if got, err := authenticate(idleBoundaryAt, idleCutoff); !errors.Is(err, pgx.ErrNoRows) || !reflect.DeepEqual(got, db.GetActiveSessionRow{}) {
		t.Fatalf("idle-boundary GetActiveSession() = (%+v, %v), want zero/no rows", got, err)
	}
	if _, err := connections[0].Exec(ctx, "UPDATE public.sessions SET last_seen_at = $1, expires_at = $2 WHERE id = $3",
		serviceTime, authenticatedAt, activeSession.SessionID); err != nil {
		t.Fatalf("set exact absolute boundary: %v", err)
	}
	if got, err := authenticate(authenticatedAt, authenticatedAt.Add(-30*time.Minute)); !errors.Is(err, pgx.ErrNoRows) || !reflect.DeepEqual(got, db.GetActiveSessionRow{}) {
		t.Fatalf("expiry-boundary GetActiveSession() = (%+v, %v), want zero/no rows", got, err)
	}
	if _, err := connections[0].Exec(ctx, "UPDATE public.sessions SET expires_at = $1 WHERE id = $2", serviceExpiresAt, activeSession.SessionID); err != nil {
		t.Fatalf("restore active-session expiry: %v", err)
	}
	if _, err := connections[0].Exec(ctx, "UPDATE public.users SET suspended_at = $1, suspended_until = NULL, suspension_reason = 'integration test' WHERE id = $2",
		serviceTime, activeSession.UserID); err != nil {
		t.Fatalf("suspend active user: %v", err)
	}
	if got, err := authenticate(authenticatedAt, authenticatedAt.Add(-30*time.Minute)); !errors.Is(err, pgx.ErrNoRows) || !reflect.DeepEqual(got, db.GetActiveSessionRow{}) {
		t.Fatalf("suspended GetActiveSession() = (%+v, %v), want zero/no rows", got, err)
	}
	if _, err := connections[0].Exec(ctx, "UPDATE public.users SET suspended_until = $1 WHERE id = $2", authenticatedAt, activeSession.UserID); err != nil {
		t.Fatalf("set exact suspension end: %v", err)
	}
	if got, err := authenticate(authenticatedAt, authenticatedAt.Add(-30*time.Minute)); err != nil || got.SessionID != activeSession.SessionID {
		t.Fatalf("ended-suspension GetActiveSession() = (%+v, %v)", got, err)
	}
	wrongTokenHash := sha256.Sum256([]byte("wrong-token"))
	for _, test := range []struct {
		name   string
		params db.RevokeSessionParams
	}{
		{name: "wrong token", params: db.RevokeSessionParams{ObservedAt: pgtype.Timestamptz{Time: authenticatedAt, Valid: true}, TokenHash: wrongTokenHash[:]}},
		{name: "before issue", params: db.RevokeSessionParams{ObservedAt: pgtype.Timestamptz{Time: serviceTime.Add(-time.Microsecond), Valid: true}, TokenHash: serviceTokenHash[:]}},
	} {
		revoked, err := service.queries.RevokeSession(ctx, test.params)
		if err != nil || revoked != 0 {
			t.Fatalf("%s RevokeSession() = (%d, %v), want (0, nil)", test.name, revoked, err)
		}
	}
	service.clock = func() time.Time { return authenticatedAt }
	if revoked, err := service.RevokeSession(ctx, serviceToken); err != nil || !revoked {
		t.Fatalf("Service.RevokeSession() = (%t, %v), want true/nil", revoked, err)
	}
	service.clock = func() time.Time { return authenticatedAt.Add(time.Minute) }
	if revoked, err := service.RevokeSession(ctx, serviceToken); err != nil || revoked {
		t.Fatalf("repeated Service.RevokeSession() = (%t, %v), want false/nil", revoked, err)
	}
	if got, err := authenticate(authenticatedAt, authenticatedAt.Add(-30*time.Minute)); !errors.Is(err, pgx.ErrNoRows) || !reflect.DeepEqual(got, db.GetActiveSessionRow{}) {
		t.Fatalf("revoked GetActiveSession() = (%+v, %v), want zero/no rows", got, err)
	}
	if touched, err := service.queries.TouchSession(ctx, db.TouchSessionParams{
		ObservedAt: pgtype.Timestamptz{Time: authenticatedAt.Add(time.Minute), Valid: true},
		SessionID:  activeSession.SessionID,
		TouchBefore: pgtype.Timestamptz{
			Time: authenticatedAt.Add(time.Minute), Valid: true,
		},
	}); err != nil || touched != 0 {
		t.Fatalf("revoked TouchSession() = (%d, %v), want zero/nil", touched, err)
	}

	revalidationInitialAt := authenticatedAt.Add(5 * time.Minute)
	revalidationClaims := verifiedIdentityClaims{
		issuer: harness.issuer, subject: "subject-1", displayName: "Danny Hunn",
	}
	revalidationOld, err := createInitialSession(
		ctx, connections[0], bytes.NewReader(bytes.Repeat([]byte{0x61}, sessionTokenBytes)),
		func() time.Time { return revalidationInitialAt }, 24*time.Hour, revalidationClaims,
	)
	if err != nil {
		t.Fatalf("create revalidation source session: %v", err)
	}
	revalidationAt := revalidationInitialAt.Add(time.Minute)
	service.clock = func() time.Time { return revalidationAt }
	revalidationMaterial, err := beginRevalidation(
		ctx, service.queries.InsertOIDCLoginAttempt, service.entropy, service.clock,
		service.validateReturnPath, revalidationOld.sessionID, serviceReturnPath,
	)
	if err != nil {
		t.Fatalf("beginRevalidation() service completion attempt: %v", err)
	}
	harness.material = revalidationMaterial
	revalidatedToken, revalidatedPath, revalidatedExpiry, err := service.CompleteRevalidation(
		ctx, revalidationMaterial.state, "service-revalidation-success", revalidationOld.token,
	)
	decodedRevalidatedToken, decodeErr := base64.RawURLEncoding.Strict().DecodeString(revalidatedToken)
	if err != nil || decodeErr != nil || len(decodedRevalidatedToken) != sessionTokenBytes ||
		revalidatedPath != serviceReturnPath || !revalidatedExpiry.Equal(revalidationAt.Add(24*time.Hour)) ||
		harness.tokenRequestCount("service-revalidation-success") != 1 {
		t.Fatalf("Service.CompleteRevalidation() = (token bytes %d, path %q, expiry %s, requests %d, errors %v/%v)",
			len(decodedRevalidatedToken), revalidatedPath, revalidatedExpiry,
			harness.tokenRequestCount("service-revalidation-success"), err, decodeErr)
	}
	revalidationOldHash := sha256.Sum256([]byte(revalidationOld.token))
	revalidatedHash := sha256.Sum256([]byte(revalidatedToken))
	revalidationObservedAt := pgtype.Timestamptz{Time: revalidationAt, Valid: true}
	revalidationIdleCutoff := pgtype.Timestamptz{Time: revalidationAt.Add(-30 * time.Minute), Valid: true}
	if got, err := service.queries.GetActiveSession(ctx, db.GetActiveSessionParams{
		TokenHash: revalidationOldHash[:], ObservedAt: revalidationObservedAt, IdleCutoff: revalidationIdleCutoff,
	}); !errors.Is(err, pgx.ErrNoRows) || !reflect.DeepEqual(got, db.GetActiveSessionRow{}) {
		t.Fatalf("service old revalidated session = (%+v, %v), want zero/no rows", got, err)
	}
	if got, err := service.queries.GetActiveSession(ctx, db.GetActiveSessionParams{
		TokenHash: revalidatedHash[:], ObservedAt: revalidationObservedAt, IdleCutoff: revalidationIdleCutoff,
	}); err != nil || got.SessionID <= 0 || got.SessionID == revalidationOld.sessionID || got.UserID != revalidationOld.userID ||
		!got.ValidatedAt.Time.Equal(revalidationAt) || !got.ExpiresAt.Time.Equal(revalidatedExpiry) {
		t.Fatalf("service replacement revalidated session = (%+v, %v)", got, err)
	}
	revalidationReplayToken, revalidationReplayPath, revalidationReplayExpiry, err := service.CompleteRevalidation(
		ctx, revalidationMaterial.state, "service-revalidation-success", revalidationOld.token,
	)
	if err == nil || revalidationReplayToken != "" || revalidationReplayPath != "" || !revalidationReplayExpiry.IsZero() ||
		harness.tokenRequestCount("service-revalidation-success") != 1 {
		t.Fatalf("replayed Service.CompleteRevalidation() = (%q, %q, %s, requests %d, %v)",
			revalidationReplayToken, revalidationReplayPath, revalidationReplayExpiry,
			harness.tokenRequestCount("service-revalidation-success"), err)
	}
}
