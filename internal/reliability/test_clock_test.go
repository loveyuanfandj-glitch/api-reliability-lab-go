package reliability

import (
	"sync"
	"time"
)

type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1_700_000_000, 0)}
}

func (clock *manualClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *manualClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	clock.mu.Unlock()
}

func (clock *manualClock) Rewind(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(-duration)
	clock.mu.Unlock()
}
