// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

import (
	"context"
	"runtime"

	"golang.org/x/sync/semaphore"
)

//////
// Vars, consts, and types.
//////

// MapFunc is the type of the function that will be executed concurrently for each
// element in a slice of type `T`. The function takes a `context.Context` and a
// value of type `T`, and returns a value of type `Result` and an error value.
type MapFunc[T any, Result any] func(context.Context, T) (Result, error)

//////
// Exported functionalities.
//////

// Map calls the `Func` concurrently on each element of `sl`, and returns the
// results and any errors that occurred.
//
// NOTE: For more info. see the `MapCh` function.
//
// NOTE: Order is preserved.
func Map[T any, Result any](
	ctx context.Context,
	sl []T,
	f MapFunc[T, Result],
	opts ...Func,
) ([]Result, Errors) {
	// Calls MapCh, and closes the channel.
	resultsCh := MapCh(ctx, sl, f, opts...)
	defer close(resultsCh)

	results := make([]Result, len(sl))

	var errs []error

	for range sl {
		result := <-resultsCh

		if result.Error != nil {
			errs = append(errs, result.Error)
		} else {
			results[result.Index] = result.Output
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}

	return results, nil
}

// MapCh is a generic function in Go that applies a function concurrently to
// each element in a slice and sends the results via a channel. It uses a
// semaphore to control the concurrency level.
//
// The function takes four parameters:
// - ctx: A context.Context which is used to control the cancellation of the
// computation.
// - concurrency: An int64 that represents the maximum number of concurrent
// goroutines that can run. If set to 0 or -1, it will use the runtime.NumCPU().
// - sl: A slice of elements of type T. The function will be applied to each of
// these elements.
// - f: A MapFunc function that takes a context and an element of type T and
// returns a Result of type any and an error.
//
// The function returns a channel which will receive a ResultCh struct
// containing three fields:
// - Index: The index of the input slice that the Result corresponds to.
// - Output: The Result of applying the function f to the corresponding element
// of the input slice.
// - Error: Any error that occurred while applying the function f.
//
// The function uses a semaphore to limit the number of goroutines that can run
// concurrently. Each goroutine applies the function f to an element of the
// input slice and sends the result via the resultsCh channel. If the context is
// cancelled or the semaphore cannot be acquired, an error is sent via the
// resultsCh channel. If an error occurs while applying the function f, the
// error is also sent via the resultsCh channel.
//
// NOTE: that the function does not close the resultsCh channel. It is the
// responsibility of the caller to ensure that all results are read from the
// channel to avoid a goroutine leak.
func MapCh[T any, Result any](
	ctx context.Context,
	sl []T,
	f MapFunc[T, Result],
	opts ...Func,
) chan ResultCh[Result] {
	o := Option{
		Concurrency: runtime.NumCPU(),
	}

	// Apply the options.
	for _, opt := range opts {
		o = opt(o)
	}

	sem := semaphore.NewWeighted(int64(o.Concurrency))

	resultsCh := make(chan ResultCh[Result])

	for i, t := range sl {
		t := t
		i := i
		sem := sem

		if err := sem.Acquire(ctx, 1); err != nil {
			resultsCh <- ResultCh[Result]{Index: i, Error: err}

			return resultsCh
		}

		go func(i int, t T) {
			defer sem.Release(1)

			result, err := f(ctx, t)
			if err != nil {
				resultsCh <- ResultCh[Result]{Index: i, Output: result, Error: err}

				return
			}

			resultsCh <- ResultCh[Result]{Index: i, Output: result, Error: nil}
		}(i, t)
	}

	return resultsCh
}
