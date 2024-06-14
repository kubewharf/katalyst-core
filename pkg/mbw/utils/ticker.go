package utils

import (
	"context"
	"time"
)

// TickUntilDone runs a given action at a tick rate specified by refreshRate, it returns if the context is cancelled
func TickUntilDone(ctx context.Context, refreshRate uint64, action func() error) (err error) {
	ticker := time.NewTicker(time.Duration(refreshRate) * time.Millisecond)
	defer ticker.Stop()

	for {
		// Run action
		err := action()
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			// Stop execution if context is cancelled
			return ctx.Err()
		case <-ticker.C:
			// Break out of blocking select for every tick
		}
	}
}
