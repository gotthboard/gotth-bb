package abuse

import (
	"fmt"
	"time"
)

// PublicationPolicy is the immutable durable-publication subset copied from
// the startup policy. Its fields are private so callers cannot manufacture an
// unvalidated runtime policy.
type PublicationPolicy struct {
	establishedLimit uint32
	newAccountLimit  uint32
	window           time.Duration
	newAccountPeriod time.Duration
}

// PublicationDecision is one pure fixed-window transition. A positive
// RetryAfter means the publication is rejected and the returned tuple must not
// be written. Otherwise StartedAt and Count are the complete replacement
// tuple.
type PublicationDecision struct {
	StartedAt  time.Time
	Count      int32
	RetryAfter time.Duration
}

// NewPublicationPolicy validates and copies the durable publication limits.
// Production obtains the same value from Policy.PublicationPolicy; the direct
// constructor supports isolated services and tests without exposing fields.
func NewPublicationPolicy(establishedLimit, newAccountLimit uint32, window, newAccountPeriod time.Duration) (PublicationPolicy, error) {
	policy := PublicationPolicy{
		establishedLimit: establishedLimit,
		newAccountLimit:  newAccountLimit,
		window:           window,
		newAccountPeriod: newAccountPeriod,
	}
	if !policy.Valid() {
		return PublicationPolicy{}, fmt.Errorf("publication policy is invalid")
	}
	return policy, nil
}

// PublicationPolicy returns the validated immutable publication policy.
func (policy Policy) PublicationPolicy() PublicationPolicy {
	return PublicationPolicy{
		establishedLimit: policy.publicationLimit,
		newAccountLimit:  policy.newAccountLimit,
		window:           policy.publicationWindow,
		newAccountPeriod: policy.newAccountPeriod,
	}
}

// Valid reports whether the profile could only have come from a successfully
// loaded startup policy.
func (policy PublicationPolicy) Valid() bool {
	return policy.establishedLimit > 0 && policy.establishedLimit <= 100_000 &&
		policy.newAccountLimit > 0 && policy.newAccountLimit <= policy.establishedLimit &&
		policy.window >= time.Second && policy.window <= 24*time.Hour &&
		policy.newAccountPeriod >= time.Minute && policy.newAccountPeriod <= 30*24*time.Hour
}

// DecidePublication validates one database-owned tuple and computes its one
// replacement or bounded rejection. Database timestamps must be finite before
// calling this pure boundary.
//
// Complexity: tight Theta(1) time and auxiliary space.
func (policy PublicationPolicy) DecidePublication(
	createdAt time.Time,
	databaseNow time.Time,
	windowStartedAt *time.Time,
	count int32,
) (PublicationDecision, error) {
	if !policy.Valid() || createdAt.IsZero() || databaseNow.IsZero() {
		return PublicationDecision{}, fmt.Errorf("publication admission state is invalid")
	}
	createdAt = createdAt.UTC()
	databaseNow = databaseNow.UTC()
	if databaseNow.Before(createdAt) {
		return PublicationDecision{}, fmt.Errorf("publication database time precedes account creation")
	}
	accountAge := databaseNow.Sub(createdAt)
	if accountAge < 0 {
		return PublicationDecision{}, fmt.Errorf("publication account age overflowed")
	}
	limit := policy.establishedLimit
	if accountAge < policy.newAccountPeriod {
		limit = policy.newAccountLimit
	}
	if windowStartedAt == nil {
		if count != 0 {
			return PublicationDecision{}, fmt.Errorf("publication window tuple is invalid")
		}
		return PublicationDecision{StartedAt: databaseNow, Count: 1}, nil
	}
	startedAt := windowStartedAt.UTC()
	if startedAt.IsZero() || startedAt.Before(createdAt) || count < 1 || count > 100_000 {
		return PublicationDecision{}, fmt.Errorf("publication window tuple is invalid")
	}
	if startedAt.After(databaseNow) {
		future := startedAt.Sub(databaseNow)
		if future <= 0 || future > policy.window {
			return PublicationDecision{}, fmt.Errorf("publication window clock regression is invalid")
		}
		if uint32(count) < limit {
			return PublicationDecision{StartedAt: startedAt, Count: count + 1}, nil
		}
		return PublicationDecision{RetryAfter: policy.window}, nil
	}
	age := databaseNow.Sub(startedAt)
	if age < 0 {
		return PublicationDecision{}, fmt.Errorf("publication window age overflowed")
	}
	if age >= policy.window {
		return PublicationDecision{StartedAt: databaseNow, Count: 1}, nil
	}
	if uint32(count) < limit {
		return PublicationDecision{StartedAt: startedAt, Count: count + 1}, nil
	}
	remaining := policy.window - age
	if remaining <= 0 || remaining > policy.window {
		return PublicationDecision{}, fmt.Errorf("publication retry interval is invalid")
	}
	return PublicationDecision{RetryAfter: remaining}, nil
}

// RetryAfterSeconds rounds a bounded rejection up to a whole second.
func (decision PublicationDecision) RetryAfterSeconds() int64 {
	if decision.RetryAfter <= 0 {
		return 0
	}
	seconds := int64(decision.RetryAfter / time.Second)
	if decision.RetryAfter%time.Second != 0 {
		seconds++
	}
	return seconds
}
