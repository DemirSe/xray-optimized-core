package task

import (
	"context"
)

// OnSuccess executes g() after f() returns nil.
func OnSuccess(f func() error, g func() error) func() error {
	return func() error {
		if err := f(); err != nil {
			return err
		}
		return g()
	}
}

// Run executes a list of tasks in parallel, returns the first error encountered or nil if all tasks pass.
func Run(ctx context.Context, tasks ...func() error) error {
	n := len(tasks)
	// ponytail: raw chan instead of semaphore.Instance (identical acquire/release).
	s := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		s <- struct{}{}
	}
	done := make(chan error, 1)

	for _, task := range tasks {
		<-s
		go func(f func() error) {
			err := f()
			if err == nil {
				s <- struct{}{}
				return
			}

			select {
			case done <- err:
			default:
			}
		}(task)
	}

	/*
		if altctx := ctx.Value("altctx"); altctx != nil {
			ctx = altctx.(context.Context)
		}
	*/

	for i := 0; i < n; i++ {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-s:
		}
	}

	/*
		if cancel := ctx.Value("cancel"); cancel != nil {
			cancel.(context.CancelFunc)()
		}
	*/

	return nil
}
