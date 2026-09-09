package task

import (
	"context"
)

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

	for i := 0; i < n; i++ {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-s:
		}
	}

	return nil
}
