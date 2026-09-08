package abuse

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io"
	"net/netip"
	"sync"
	"time"
)

const requestClientDomain = "gotth-bb/request-client/v1"

// RequestDecision identifies the only three request-admission outcomes.
type RequestDecision uint8

const (
	RequestAllowed RequestDecision = iota
	RequestRateLimited
	RequestCapacityLimited
)

type requestWindow struct {
	started time.Time
	count   uint32
}

// RequestLimiter retains fixed-window state for a bounded number of digested
// client addresses. It starts no goroutine and owns no raw client address.
type RequestLimiter struct {
	mutex         sync.Mutex
	key           [32]byte
	entries       map[[32]byte]requestWindow
	capacity      uint32
	limit         uint32
	window        time.Duration
	clock         func() time.Time
	earliest      time.Time
	earliestValid bool
	digest        func([32]byte, netip.Addr) [32]byte
}

// NewRequestLimiter reads exactly one process key and constructs bounded state.
func NewRequestLimiter(entropy io.Reader, capacity, limit uint32, window time.Duration, clock func() time.Time) (*RequestLimiter, error) {
	if entropy == nil || clock == nil || capacity == 0 || capacity > 65_536 || limit == 0 || limit > 100_000 || window < time.Second || window > 24*time.Hour {
		return nil, fmt.Errorf("request limiter configuration is invalid")
	}
	limiter := &RequestLimiter{
		entries: make(map[[32]byte]requestWindow, capacity), capacity: capacity,
		limit: limit, window: window, clock: clock, digest: requestDigest,
	}
	if _, err := io.ReadFull(entropy, limiter.key[:]); err != nil {
		return nil, fmt.Errorf("request limiter entropy is unavailable")
	}
	return limiter, nil
}

// NewRequestLimiter constructs the policy's bounded process-local request
// state with one fresh 256-bit digest key.
func (policy Policy) NewRequestLimiter(entropy io.Reader, clock func() time.Time) (*RequestLimiter, error) {
	return NewRequestLimiter(entropy, policy.requestClientCapacity, policy.requestLimit, policy.requestWindow, clock)
}

// Admit charges one canonical address and returns a bounded retry duration.
func (limiter *RequestLimiter) Admit(address netip.Addr) (RequestDecision, time.Duration) {
	if limiter == nil || !address.IsValid() || address.Zone() != "" {
		return RequestCapacityLimited, time.Second
	}
	address = address.Unmap()
	now := limiter.clock()
	digest := limiter.digest(limiter.key, address)
	limiter.mutex.Lock()
	defer limiter.mutex.Unlock()

	if entry, exists := limiter.entries[digest]; exists {
		if !now.Before(entry.started.Add(limiter.window)) {
			limiter.entries[digest] = requestWindow{started: now, count: 1}
			limiter.earliestValid = false
			return RequestAllowed, 0
		}
		if entry.count >= limiter.limit {
			return RequestRateLimited, boundedRetry(entry.started, now, limiter.window)
		}
		entry.count++
		limiter.entries[digest] = entry
		return RequestAllowed, 0
	}

	if uint32(len(limiter.entries)) >= limiter.capacity {
		if limiter.earliestValid && now.Before(limiter.earliest) {
			return RequestCapacityLimited, time.Second
		}
		limiter.reclaim(now)
		if uint32(len(limiter.entries)) >= limiter.capacity {
			return RequestCapacityLimited, time.Second
		}
	}

	limiter.entries[digest] = requestWindow{started: now, count: 1}
	expires := now.Add(limiter.window)
	if !limiter.earliestValid || expires.Before(limiter.earliest) {
		limiter.earliest = expires
		limiter.earliestValid = true
	}
	return RequestAllowed, 0
}

func (limiter *RequestLimiter) reclaim(now time.Time) {
	limiter.earliest = time.Time{}
	limiter.earliestValid = false
	for digest, entry := range limiter.entries {
		expires := entry.started.Add(limiter.window)
		if !now.Before(expires) {
			delete(limiter.entries, digest)
			continue
		}
		if !limiter.earliestValid || expires.Before(limiter.earliest) {
			limiter.earliest = expires
			limiter.earliestValid = true
		}
	}
}

func requestDigest(key [32]byte, address netip.Addr) [32]byte {
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(requestClientDomain))
	_, _ = mac.Write([]byte{0})
	if address.Is4() {
		value := address.As4()
		_, _ = mac.Write(value[:])
	} else {
		value := address.As16()
		_, _ = mac.Write(value[:])
	}
	var digest [32]byte
	copy(digest[:], mac.Sum(nil))
	return digest
}

func boundedRetry(started, now time.Time, window time.Duration) time.Duration {
	elapsed := now.Sub(started)
	if elapsed >= window {
		return time.Second
	}
	remaining := window
	if elapsed > 0 {
		remaining -= elapsed
	}
	seconds := remaining / time.Second
	if remaining%time.Second != 0 {
		seconds++
	}
	if seconds < 1 {
		seconds = 1
	}
	return seconds * time.Second
}
