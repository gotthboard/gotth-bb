package abuse

import (
	"bytes"
	"fmt"
	"io"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestLimiterFixedWindowsAndRetry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	limiter, err := NewRequestLimiter(bytes.NewReader(bytes.Repeat([]byte{0x41}, 32)), 2, 2, 1500*time.Millisecond, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewRequestLimiter() returned error: %v", err)
	}
	client := netip.MustParseAddr("192.0.2.1")
	for call := 1; call <= 2; call++ {
		if decision, retry := limiter.Admit(client); decision != RequestAllowed || retry != 0 {
			t.Fatalf("Admit() call %d = (%d, %s)", call, decision, retry)
		}
	}
	if decision, retry := limiter.Admit(client); decision != RequestRateLimited || retry != 2*time.Second {
		t.Fatalf("limited Admit() = (%d, %s)", decision, retry)
	}
	now = now.Add(time.Second)
	if decision, retry := limiter.Admit(client); decision != RequestRateLimited || retry != time.Second {
		t.Fatalf("part-window Admit() = (%d, %s)", decision, retry)
	}
	now = now.Add(500 * time.Millisecond)
	if decision, retry := limiter.Admit(client); decision != RequestAllowed || retry != 0 {
		t.Fatalf("new-window Admit() = (%d, %s)", decision, retry)
	}
}

func TestRequestLimiterCapacityReclaimsOnlyExpiredWindows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	limiter, err := NewRequestLimiter(bytes.NewReader(bytes.Repeat([]byte{0x42}, 32)), 2, 1, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewRequestLimiter() returned error: %v", err)
	}
	first := netip.MustParseAddr("192.0.2.1")
	second := netip.MustParseAddr("2001:db8::2")
	third := netip.MustParseAddr("192.0.2.3")
	for _, client := range []netip.Addr{first, second} {
		if decision, retry := limiter.Admit(client); decision != RequestAllowed || retry != 0 {
			t.Fatalf("Admit(%s) = (%d, %s)", client, decision, retry)
		}
	}
	if decision, retry := limiter.Admit(third); decision != RequestCapacityLimited || retry != time.Second {
		t.Fatalf("capacity Admit() = (%d, %s)", decision, retry)
	}
	now = now.Add(time.Minute)
	if decision, retry := limiter.Admit(third); decision != RequestAllowed || retry != 0 {
		t.Fatalf("reclaimed Admit() = (%d, %s)", decision, retry)
	}
	if len(limiter.entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(limiter.entries))
	}
}

func TestRequestLimiterRestartDropsProcessLocalWindows(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	client := netip.MustParseAddr("192.0.2.90")
	newLimiter := func() *RequestLimiter {
		limiter, err := NewRequestLimiter(bytes.NewReader(bytes.Repeat([]byte{0x46}, 32)), 1, 1, time.Minute, func() time.Time { return now })
		if err != nil {
			t.Fatalf("NewRequestLimiter() returned error: %v", err)
		}
		return limiter
	}
	first := newLimiter()
	if decision, _ := first.Admit(client); decision != RequestAllowed {
		t.Fatalf("first admission = %d", decision)
	}
	if decision, _ := first.Admit(client); decision != RequestRateLimited {
		t.Fatalf("limited admission = %d", decision)
	}
	if decision, retry := newLimiter().Admit(client); decision != RequestAllowed || retry != 0 {
		t.Fatalf("post-restart admission = (%d, %s)", decision, retry)
	}
}

func TestRequestLimiterCollisionConservativelySharesOneWindow(t *testing.T) {
	t.Parallel()
	limiter, err := NewRequestLimiter(bytes.NewReader(bytes.Repeat([]byte{0x47}, 32)), 2, 1, time.Minute, time.Now)
	if err != nil {
		t.Fatalf("NewRequestLimiter() returned error: %v", err)
	}
	limiter.digest = func([32]byte, netip.Addr) [32]byte { return [32]byte{1} }
	if decision, _ := limiter.Admit(netip.MustParseAddr("192.0.2.1")); decision != RequestAllowed {
		t.Fatalf("first colliding admission = %d", decision)
	}
	if decision, retry := limiter.Admit(netip.MustParseAddr("2001:db8::1")); decision != RequestRateLimited || retry < time.Second || retry > time.Minute {
		t.Fatalf("second colliding admission = (%d, %s)", decision, retry)
	}
	if len(limiter.entries) != 1 {
		t.Fatalf("colliding entry count = %d", len(limiter.entries))
	}
}

func TestRequestLimiterReclaimRetainsLiveWindowsAndEarliestBoundary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	limiter, err := NewRequestLimiter(bytes.NewReader(bytes.Repeat([]byte{0x48}, 32)), 2, 10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewRequestLimiter() returned error: %v", err)
	}
	first := netip.MustParseAddr("192.0.2.1")
	second := netip.MustParseAddr("192.0.2.2")
	third := netip.MustParseAddr("192.0.2.3")
	fourth := netip.MustParseAddr("192.0.2.4")
	limiter.Admit(first)
	now = now.Add(30 * time.Second)
	limiter.Admit(second)
	now = now.Add(30 * time.Second)
	if decision, _ := limiter.Admit(third); decision != RequestAllowed {
		t.Fatalf("reclaiming admission = %d", decision)
	}
	if len(limiter.entries) != 2 {
		t.Fatalf("entry count after partial reclaim = %d", len(limiter.entries))
	}
	if decision, _ := limiter.Admit(fourth); decision != RequestCapacityLimited {
		t.Fatalf("pre-earliest capacity admission = %d", decision)
	}
	now = now.Add(30 * time.Second)
	if decision, _ := limiter.Admit(fourth); decision != RequestAllowed {
		t.Fatalf("next-boundary admission = %d", decision)
	}
}

func TestRequestLimiterCanonicalIdentityAndClockRegression(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	limiter, err := NewRequestLimiter(bytes.NewReader(bytes.Repeat([]byte{0x43}, 32)), 2, 1, 1500*time.Millisecond, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewRequestLimiter() returned error: %v", err)
	}
	if decision, _ := limiter.Admit(netip.MustParseAddr("::ffff:192.0.2.9")); decision != RequestAllowed {
		t.Fatalf("mapped admission = %d", decision)
	}
	if decision, retry := limiter.Admit(netip.MustParseAddr("192.0.2.9")); decision != RequestRateLimited || retry != 2*time.Second {
		t.Fatalf("unmapped admission = (%d, %s)", decision, retry)
	}
	now = time.Unix(-1<<62, 0)
	if decision, retry := limiter.Admit(netip.MustParseAddr("192.0.2.9")); decision != RequestRateLimited || retry != 2*time.Second {
		t.Fatalf("regressed-clock admission = (%d, %s)", decision, retry)
	}
}

func TestRequestLimiterConcurrentAdmissionIsExact(t *testing.T) {
	t.Parallel()
	limiter, err := NewRequestLimiter(bytes.NewReader(bytes.Repeat([]byte{0x44}, 32)), 1, 25, time.Minute, func() time.Time {
		return time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	})
	if err != nil {
		t.Fatalf("NewRequestLimiter() returned error: %v", err)
	}
	client := netip.MustParseAddr("192.0.2.10")
	decisions := make(chan RequestDecision, 100)
	var group sync.WaitGroup
	for range 100 {
		group.Add(1)
		go func() {
			defer group.Done()
			decision, _ := limiter.Admit(client)
			decisions <- decision
		}()
	}
	group.Wait()
	close(decisions)
	allowed := 0
	limited := 0
	for decision := range decisions {
		switch decision {
		case RequestAllowed:
			allowed++
		case RequestRateLimited:
			limited++
		default:
			t.Fatalf("unexpected decision %d", decision)
		}
	}
	if allowed != 25 || limited != 75 {
		t.Fatalf("decisions = allowed %d, limited %d", allowed, limited)
	}
}

func TestRequestLimiterAdmissionPopulationEvidence(t *testing.T) {
	if os.Getenv("GOTTH_BB_RUN_AN05_ADMISSION_EVIDENCE") != "1" {
		t.Skip("set GOTTH_BB_RUN_AN05_ADMISSION_EVIDENCE=1 on the designated evidence host")
	}
	const capacity = 4096
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	limiter, err := NewRequestLimiter(bytes.NewReader(bytes.Repeat([]byte{0x55}, 32)), capacity, 1, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewRequestLimiter() returned error: %v", err)
	}
	clients := make([]netip.Addr, capacity)
	for index := range clients {
		clients[index] = netip.MustParseAddr(fmt.Sprintf("2001:db8::%x", index+1))
	}
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	beforeRSS := requestLimiterRSSKiB()
	started := time.Now()
	start := make(chan struct{})
	decisions := make(chan RequestDecision, capacity)
	var group sync.WaitGroup
	for _, client := range clients {
		client := client
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			decision, retry := limiter.Admit(client)
			if retry != 0 {
				decisions <- RequestCapacityLimited
				return
			}
			decisions <- decision
		}()
	}
	close(start)
	group.Wait()
	close(decisions)
	allowed := 0
	for decision := range decisions {
		if decision != RequestAllowed {
			t.Fatalf("population admission decision = %d", decision)
		}
		allowed++
	}
	if decision, retry := limiter.Admit(netip.MustParseAddr("2001:db8::ffff")); decision != RequestCapacityLimited || retry != time.Second {
		t.Fatalf("capacity+1 admission = (%d, %s)", decision, retry)
	}
	if allowed != capacity || len(limiter.entries) != capacity {
		t.Fatalf("population result = allowed %d entries %d", allowed, len(limiter.entries))
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	t.Logf("AN05_REQUEST_POPULATION clients=%d elapsed=%s map_cardinality=%d heap_alloc_delta=%d total_alloc_delta=%d sys_delta=%d rss_kib_before=%s rss_kib_after=%s capacity_plus_one=service-unavailable",
		allowed, time.Since(started), len(limiter.entries), int64(after.HeapAlloc)-int64(before.HeapAlloc), after.TotalAlloc-before.TotalAlloc,
		int64(after.Sys)-int64(before.Sys), beforeRSS, requestLimiterRSSKiB())
}

func requestLimiterRSSKiB() string {
	contents, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "unavailable"
	}
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmRSS:"))
		}
	}
	return "unavailable"
}

func TestNewRequestLimiterRejectsInvalidInputsWithoutRetainingPartialState(t *testing.T) {
	t.Parallel()
	validEntropy := bytes.NewReader(bytes.Repeat([]byte{0x45}, 32))
	for _, test := range []struct {
		name     string
		entropy  io.Reader
		capacity uint32
		limit    uint32
		window   time.Duration
		clock    func() time.Time
	}{
		{name: "nil entropy", capacity: 1, limit: 1, window: time.Second, clock: time.Now},
		{name: "short entropy", entropy: bytes.NewReader([]byte("short")), capacity: 1, limit: 1, window: time.Second, clock: time.Now},
		{name: "zero capacity", entropy: validEntropy, limit: 1, window: time.Second, clock: time.Now},
		{name: "large capacity", entropy: validEntropy, capacity: 65_537, limit: 1, window: time.Second, clock: time.Now},
		{name: "zero limit", entropy: validEntropy, capacity: 1, window: time.Second, clock: time.Now},
		{name: "large limit", entropy: validEntropy, capacity: 1, limit: 100_001, window: time.Second, clock: time.Now},
		{name: "short window", entropy: validEntropy, capacity: 1, limit: 1, window: time.Second - 1, clock: time.Now},
		{name: "long window", entropy: validEntropy, capacity: 1, limit: 1, window: 24*time.Hour + 1, clock: time.Now},
		{name: "nil clock", entropy: validEntropy, capacity: 1, limit: 1, window: time.Second},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			limiter, err := NewRequestLimiter(test.entropy, test.capacity, test.limit, test.window, test.clock)
			if err == nil || limiter != nil {
				t.Fatalf("NewRequestLimiter() = (%v, %v)", limiter, err)
			}
		})
	}
	if decision, retry := (*RequestLimiter)(nil).Admit(netip.Addr{}); decision != RequestCapacityLimited || retry != time.Second {
		t.Fatalf("nil limiter admission = (%d, %s)", decision, retry)
	}
}
