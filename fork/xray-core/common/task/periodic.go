package task

import (
	"sync"
	"time"
)

// Periodic runs Execute immediately on Start, then every Interval until
// Close or an Execute error. Start is idempotent while running and
// restarts after Close.
type Periodic struct {
	// Interval of the task being run
	Interval time.Duration
	// Execute is the task function
	Execute func() error

	mu      sync.Mutex
	timer   *time.Timer
	running bool
}

func (t *Periodic) step() error {
	t.mu.Lock()
	if !t.running {
		t.mu.Unlock()
		return nil
	}
	t.mu.Unlock()

	if err := t.Execute(); err != nil {
		t.mu.Lock()
		t.running = false
		t.mu.Unlock()
		return err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		t.timer = time.AfterFunc(t.Interval, func() { _ = t.step() })
	}
	return nil
}

// Start implements common.Runnable.
func (t *Periodic) Start() error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return nil
	}
	t.running = true
	t.mu.Unlock()

	return t.step()
}

// Close implements common.Closable.
func (t *Periodic) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.running = false
	if t.timer != nil {
		t.timer.Stop()
		t.timer = nil
	}

	return nil
}
