package abuse

import (
	"testing"
	"time"
)

func TestNewPublicationPolicyAcceptsOnlyValidatedLimits(t *testing.T) {
	t.Parallel()
	policy, err := NewPublicationPolicy(10, 3, 10*time.Minute, 24*time.Hour)
	if err != nil || !policy.Valid() {
		t.Fatalf("NewPublicationPolicy(valid) = (%+v, %v)", policy, err)
	}
	if policy, err := NewPublicationPolicy(0, 3, 10*time.Minute, 24*time.Hour); err == nil || policy.Valid() {
		t.Fatalf("NewPublicationPolicy(invalid) = (%+v, %v)", policy, err)
	}
}

func TestDecidePublicationAppliesNewAndEstablishedLimits(t *testing.T) {
	t.Parallel()
	policy := PublicationPolicy{establishedLimit: 10, newAccountLimit: 3, window: 10 * time.Minute, newAccountPeriod: 24 * time.Hour}
	created := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	started := created.Add(time.Hour)

	for _, test := range []struct {
		name    string
		now     time.Time
		count   int32
		limited bool
		want    int32
	}{
		{name: "new below", now: started.Add(time.Minute), count: 2, want: 3},
		{name: "new full", now: started.Add(time.Minute), count: 3, limited: true},
		{name: "new above configured limit", now: started.Add(time.Minute), count: 4, limited: true},
		{name: "period equality established", now: created.Add(24 * time.Hour), count: 3, want: 4},
		{name: "established below", now: created.Add(24*time.Hour + time.Minute), count: 9, want: 10},
		{name: "established full", now: created.Add(24*time.Hour + time.Minute), count: 10, limited: true},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			window := started
			if !test.now.Before(created.Add(24 * time.Hour)) {
				window = created.Add(23*time.Hour + 59*time.Minute)
			}
			decision, err := policy.DecidePublication(created, test.now, &window, test.count)
			if err != nil || (decision.RetryAfter > 0) != test.limited || decision.Count != test.want {
				t.Fatalf("DecidePublication() = (%+v, %v), want limited %t count %d", decision, err, test.limited, test.want)
			}
			if test.limited && (decision.RetryAfterSeconds() <= 0 || decision.RetryAfter > policy.window) {
				t.Fatalf("limited retry = %s/%d", decision.RetryAfter, decision.RetryAfterSeconds())
			}
		})
	}
}

func TestDecidePublicationResetsAndHandlesBoundedClockRegression(t *testing.T) {
	t.Parallel()
	policy := PublicationPolicy{establishedLimit: 10, newAccountLimit: 3, window: 10 * time.Minute, newAccountPeriod: 24 * time.Hour}
	created := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	now := created.Add(48 * time.Hour)

	decision, err := policy.DecidePublication(created, now, nil, 0)
	if err != nil || decision.StartedAt != now || decision.Count != 1 || decision.RetryAfter != 0 {
		t.Fatalf("empty tuple = (%+v, %v)", decision, err)
	}
	expired := now.Add(-policy.window)
	decision, err = policy.DecidePublication(created, now, &expired, 10)
	if err != nil || decision.StartedAt != now || decision.Count != 1 {
		t.Fatalf("window equality = (%+v, %v)", decision, err)
	}
	future := now.Add(time.Minute)
	decision, err = policy.DecidePublication(created, now, &future, 9)
	if err != nil || decision.StartedAt != future || decision.Count != 10 {
		t.Fatalf("bounded future below limit = (%+v, %v)", decision, err)
	}
	decision, err = policy.DecidePublication(created, now, &future, 10)
	if err != nil || decision.RetryAfter != policy.window || decision.RetryAfterSeconds() != 600 {
		t.Fatalf("bounded future at limit = (%+v, %v)", decision, err)
	}
	exactFuture := now.Add(policy.window)
	decision, err = policy.DecidePublication(created, now, &exactFuture, 9)
	if err != nil || decision.StartedAt != exactFuture || decision.Count != 10 || decision.RetryAfter != 0 {
		t.Fatalf("exact-window future below limit = (%+v, %v)", decision, err)
	}
}

func TestDecidePublicationRejectsMalformedState(t *testing.T) {
	t.Parallel()
	valid := PublicationPolicy{establishedLimit: 10, newAccountLimit: 3, window: 10 * time.Minute, newAccountPeriod: 24 * time.Hour}
	created := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	now := created.Add(time.Hour)
	before := created.Add(-time.Microsecond)
	farFuture := now.Add(valid.window + time.Microsecond)

	for _, test := range []struct {
		name    string
		policy  PublicationPolicy
		created time.Time
		now     time.Time
		start   *time.Time
		count   int32
	}{
		{name: "invalid policy", policy: PublicationPolicy{}, created: created, now: now},
		{name: "database before creation", policy: valid, created: created, now: created.Add(-time.Microsecond)},
		{name: "empty tuple nonzero count", policy: valid, created: created, now: now, count: 1},
		{name: "start before creation", policy: valid, created: created, now: now, start: &before, count: 1},
		{name: "started tuple zero count", policy: valid, created: created, now: now, start: &created},
		{name: "count above schema", policy: valid, created: created, now: now, start: &created, count: 100001},
		{name: "far future", policy: valid, created: created, now: now, start: &farFuture, count: 1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if decision, err := test.policy.DecidePublication(test.created, test.now, test.start, test.count); err == nil || decision != (PublicationDecision{}) {
				t.Fatalf("DecidePublication() = (%+v, %v), want rejection", decision, err)
			}
		})
	}
}

func TestDecidePublicationAcceptsFinitePostgreSQLYearOne(t *testing.T) {
	t.Parallel()
	policy := PublicationPolicy{establishedLimit: 10, newAccountLimit: 3, window: 2 * time.Hour, newAccountPeriod: 24 * time.Hour}
	created := time.Time{}
	now := created.Add(time.Hour)

	decision, err := policy.DecidePublication(created, now, nil, 0)
	if err != nil || decision.StartedAt != now || decision.Count != 1 {
		t.Fatalf("year-one empty tuple = (%+v, %v)", decision, err)
	}
	started := created
	decision, err = policy.DecidePublication(created, now, &started, 1)
	if err != nil || decision.StartedAt != created || decision.Count != 2 {
		t.Fatalf("year-one active tuple = (%+v, %v)", decision, err)
	}
}

func TestDecidePublicationHandlesPostgreSQLFiniteExtremesBySubtraction(t *testing.T) {
	t.Parallel()
	policy := PublicationPolicy{establishedLimit: 2, newAccountLimit: 1, window: time.Second, newAccountPeriod: time.Minute}
	minimum := time.Date(-4712, time.January, 1, 0, 0, 0, 0, time.UTC)
	maximum := time.Date(294276, time.December, 31, 23, 59, 59, 999999000, time.UTC)

	decision, err := policy.DecidePublication(minimum, maximum, nil, 0)
	if err != nil || decision.StartedAt != maximum || decision.Count != 1 {
		t.Fatalf("finite extremes = (%+v, %v)", decision, err)
	}
	decision, err = policy.DecidePublication(maximum, maximum, nil, 0)
	if err != nil || decision.StartedAt != maximum || decision.Count != 1 {
		t.Fatalf("maximum equality = (%+v, %v)", decision, err)
	}
}

func TestDecidePublicationExposesAdmittedTwoWindowBurst(t *testing.T) {
	t.Parallel()
	policy := PublicationPolicy{establishedLimit: 3, newAccountLimit: 3, window: 10 * time.Minute, newAccountPeriod: time.Minute}
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	started := created.Add(time.Hour)

	// Three publications may occur at the end of one fixed window. At exact
	// expiry the next publication resets the tuple, allowing three more in the
	// following window. This is the admitted 2N boundary burst, not a rolling
	// limit claim.
	now := started.Add(policy.window)
	decision, err := policy.DecidePublication(created, now, &started, 3)
	if err != nil || decision.StartedAt != now || decision.Count != 1 {
		t.Fatalf("boundary reset = (%+v, %v)", decision, err)
	}
	for want := int32(2); want <= 3; want++ {
		window := decision.StartedAt
		decision, err = policy.DecidePublication(created, now, &window, decision.Count)
		if err != nil || decision.Count != want {
			t.Fatalf("next-window publication %d = (%+v, %v)", want, decision, err)
		}
	}
	window := decision.StartedAt
	if rejected, err := policy.DecidePublication(created, now, &window, decision.Count); err != nil || rejected.RetryAfter != policy.window {
		t.Fatalf("next-window limit = (%+v, %v)", rejected, err)
	}
}

func TestPublicationRetryAfterSecondsIsBoundedCeiling(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		retry time.Duration
		want  int64
	}{
		{retry: 0, want: 0},
		{retry: -time.Nanosecond, want: 0},
		{retry: time.Second, want: 1},
		{retry: time.Second + time.Nanosecond, want: 2},
	} {
		if got := (PublicationDecision{RetryAfter: test.retry}).RetryAfterSeconds(); got != test.want {
			t.Fatalf("RetryAfterSeconds(%s) = %d, want %d", test.retry, got, test.want)
		}
	}
}
