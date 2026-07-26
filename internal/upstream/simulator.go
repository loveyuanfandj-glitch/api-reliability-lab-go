package upstream

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

type Mode string

const (
	Healthy     Mode = "healthy"
	Slow        Mode = "slow"
	Flaky       Mode = "flaky"
	Unavailable Mode = "unavailable"
)

var ErrUnavailable = errors.New("synthetic inventory dependency unavailable")

type Simulator struct {
	mu    sync.RWMutex
	mode  Mode
	calls uint64
}

func NewSimulator() *Simulator { return &Simulator{mode: Healthy} }

func (s *Simulator) SetMode(mode Mode) error {
	switch mode {
	case Healthy, Slow, Flaky, Unavailable:
		s.mu.Lock()
		s.mode = mode
		s.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("invalid dependency mode %q", mode)
	}
}

func (s *Simulator) Mode() Mode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mode
}

func (s *Simulator) Calls() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.calls
}

func (s *Simulator) Reserve(ctx context.Context, eventID string, quantity int) error {
	s.mu.Lock()
	s.calls++
	call := s.calls
	mode := s.mode
	s.mu.Unlock()

	delay := 35 * time.Millisecond
	switch mode {
	case Slow:
		delay = 650 * time.Millisecond
	case Unavailable:
		delay = 15 * time.Millisecond
	case Flaky:
		delay = 45 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	if mode == Unavailable || (mode == Flaky && call%3 != 0) {
		return ErrUnavailable
	}
	return nil
}

func (s *Simulator) Reset() {
	s.mu.Lock()
	s.mode = Healthy
	s.calls = 0
	s.mu.Unlock()
}
