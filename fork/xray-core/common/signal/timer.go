package signal

import (
	"context"
	"sync"
	"time"
)

type ActivityUpdater interface {
	Update()
}

// ActivityTimer cancels its context after timeout without Update.
// ponytail: single time.Timer replaces the task.Periodic checker;
// same API, exact timeout instead of up-to-2x check granularity.
type ActivityTimer struct {
	mu        sync.Mutex
	timer     *time.Timer
	timeout   time.Duration
	onTimeout func()
	finished  bool
}

func (t *ActivityTimer) Update() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.finished {
		return
	}
	if t.timer != nil {
		t.timer.Stop()
	}
	if t.timeout > 0 {
		t.timer = time.AfterFunc(t.timeout, t.finish)
	}
}

func (t *ActivityTimer) finish() {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	t.finished = true
	t.mu.Unlock()

	t.onTimeout()
}

func (t *ActivityTimer) SetTimeout(timeout time.Duration) {
	t.mu.Lock()
	if t.finished {
		t.mu.Unlock()
		return
	}
	t.timeout = timeout
	t.mu.Unlock()

	if timeout == 0 {
		t.finish()
		return
	}
	t.Update()
}

func CancelAfterInactivity(ctx context.Context, cancel context.CancelFunc, timeout time.Duration) *ActivityTimer {
	timer := &ActivityTimer{
		onTimeout: cancel,
	}
	timer.SetTimeout(timeout)
	return timer
}
