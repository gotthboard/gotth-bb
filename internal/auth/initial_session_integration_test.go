//go:build integration

package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"git.dannyhunn.com/agents/gotth-bb/internal/migration"
	"git.dannyhunn.com/agents/gotth-bb/internal/store/db"
	"git.dannyhunn.com/agents/gotth-bb/migrations"
	"github.com/jackc/pgx/v5"
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
	serviceEntropy := bytes.NewReader(sequentialBytes(256))
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
}
