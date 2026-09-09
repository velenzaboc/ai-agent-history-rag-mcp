package watch

import (
	"context"
	"errors"
	"sort"
	"time"
)

var ErrEventQueueFull = errors.New("watch event queue full")

type Event struct {
	Path string
	At   time.Time
}
type Dispatch func(context.Context, string) error
type Loop struct {
	Debounce time.Duration
	Capacity int
	Dispatch Dispatch
}

func (l Loop) Run(ctx context.Context, events <-chan Event) error {
	if l.Dispatch == nil {
		return errors.New("watch dispatch is required")
	}
	d := l.Debounce
	if d <= 0 {
		d = time.Millisecond
	}
	cap := l.Capacity
	if cap <= 0 {
		cap = 64
	}
	pending := map[string]time.Time{}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	for {
		var tick <-chan time.Time
		if len(pending) > 0 {
			next := time.Time{}
			for _, at := range pending {
				if next.IsZero() || at.Before(next) {
					next = at
				}
			}
			timer.Reset(time.Until(next))
			tick = timer.C
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-events:
			if !ok {
				return nil
			}
			if e.Path == "" {
				continue
			}
			if len(pending) >= cap {
				if _, ok := pending[e.Path]; !ok {
					return ErrEventQueueFull
				}
			}
			pending[e.Path] = e.At.Add(d)
		case <-tick:
			paths := []string{}
			now := time.Now()
			for p, at := range pending {
				if !at.After(now) {
					paths = append(paths, p)
					delete(pending, p)
				}
			}
			sort.Strings(paths)
			for _, p := range paths {
				if err := l.Dispatch(ctx, p); err != nil {
					return err
				}
			}
		}
	}
}
