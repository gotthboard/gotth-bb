package abuse

import (
	"context"
	"fmt"
	"log/slog"
)

// RejectionClass is a fixed, non-attacker-controlled abuse outcome.
type RejectionClass string

const (
	RejectionRequestRate     RejectionClass = "request_rate"
	RejectionRequestCapacity RejectionClass = "request_capacity"
	RejectionPublicationRate RejectionClass = "publication_rate"
	RejectionBlocked         RejectionClass = "blocked_destination"
)

// RejectionRoute is a fixed operation class, never a caller-controlled URL.
type RejectionRoute string

const (
	RouteRequestAdmission RejectionRoute = "request-admission"
	RouteTopicPublication RejectionRoute = "topic-publication"
	RouteReplyPublication RejectionRoute = "reply-publication"
	RoutePostEdit         RejectionRoute = "post-edit"
	RouteCommunityRules   RejectionRoute = "community-rules"
)

// Event contains only bounded nonidentity rejection evidence.
type Event struct {
	Class        RejectionClass
	Route        RejectionRoute
	RequestID    string
	Status       int
	RetrySeconds int64
}

// Observer records one terminal abuse rejection.
type Observer interface {
	Observe(context.Context, Event)
}

type slogObserver struct{ logger *slog.Logger }

// NewObserver constructs the fixed structured rejection observer.
func NewObserver(logger *slog.Logger) (Observer, error) {
	if logger == nil {
		return nil, fmt.Errorf("abuse observer logger is required")
	}
	return slogObserver{logger: logger}, nil
}

func (observer slogObserver) Observe(ctx context.Context, event Event) {
	if !validEvent(event) {
		return
	}
	observer.logger.InfoContext(ctx, "abuse request rejected",
		"class", string(event.Class), "route", string(event.Route),
		"request_id", event.RequestID, "status", event.Status,
		"retry_seconds", event.RetrySeconds)
}

func validEvent(event Event) bool {
	if len(event.RequestID) != 32 {
		return false
	}
	for _, character := range event.RequestID {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	validRoute := event.Route == RouteRequestAdmission || event.Route == RouteTopicPublication ||
		event.Route == RouteReplyPublication || event.Route == RoutePostEdit || event.Route == RouteCommunityRules
	if !validRoute || event.RetrySeconds < 0 || event.RetrySeconds > 86_400 {
		return false
	}
	switch event.Class {
	case RejectionRequestRate:
		return event.Status == 429 && event.RetrySeconds >= 1
	case RejectionRequestCapacity:
		return event.Status == 503 && event.RetrySeconds == 1
	case RejectionPublicationRate:
		return event.Status == 429 && event.RetrySeconds >= 1
	case RejectionBlocked:
		return event.Status == 422 && event.RetrySeconds == 0
	default:
		return false
	}
}
