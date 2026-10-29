package core

import (
	"context"
	"sync"
)

// normalizedWorkers clamps a worker count to the supported 1..100 range.
func normalizedWorkers(n int) int {
	switch {
	case n < 1:
		return 1
	case n > 100:
		return 100
	default:
		return n
	}
}

// runConcurrent applies fn to every item using at most workers goroutines.
// New work stops being dispatched once ctx is cancelled. The first non-nil
// error returned by fn is treated as fatal: no further work is dispatched and
// it is returned after the in-flight work drains.
//
// Per-item failures should be reported by fn (via the Reporter) and returned as
// nil so the remaining items still run — that matches the sequential behaviour
// these phases had before.
func runConcurrent[T any](ctx context.Context, workers int, items []T, fn func(T) error) error {
	if workers < 1 {
		workers = 1
	}
	if workers > len(items) {
		workers = len(items)
	}
	if workers == 0 {
		return nil
	}

	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	errCh := make(chan error, 1) // buffered: the first fatal error wins

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		select {
		case e := <-errCh:
			wg.Wait()
			return e
		default:
		}

		sem <- struct{}{}
		wg.Add(1)
		go func(it T) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := fn(it); err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}(it)
	}
	wg.Wait()

	select {
	case e := <-errCh:
		return e
	default:
	}
	return nil
}
