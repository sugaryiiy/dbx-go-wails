package agent

import "context"

type contextMutex chan struct{}

func (m contextMutex) Acquire(ctx context.Context) error {
	select {
	case m <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-m
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m contextMutex) Lock()   { _ = m.Acquire(context.Background()) }
func (m contextMutex) Unlock() { <-m }
