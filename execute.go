// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

import (
	"context"
)

//////
// Vars, consts, and types.
//////

// ExecuteFunc is the type of the function that will be executed concurrently
// for each element in a slice of type `T`. The function takes a `context.Context`
// and a value of type `T`, and returns a value of type `Result` and an error
// value.
type ExecuteFunc[T any] func(context.Context) (T, error)

//////
// Exported functionalities.
//////

// Execute calls the `fns` concurrently, and returns the results and any errors
// that occurred. The function blocks until all executions have completed.
func Execute[T any](ctx context.Context, fns []ExecuteFunc[T]) ([]T, Errors) {
	// Calls runCh, and closes the channel.
	resultsCh := ExecuteCh(ctx, fns)
	defer close(resultsCh)

	var (
		results []T
		errs    []error
	)

	for range fns {
		result := <-resultsCh

		if result.Error != nil {
			errs = append(errs, result.Error)
		} else {
			results = append(results, result.Output)
		}
	}

	return results, errs
}

// ExecuteCh calls the `fns` concurrently.
//
// NOTE: It's the caller's responsibility to close the channel.
func ExecuteCh[T any](ctx context.Context, fns []ExecuteFunc[T]) chan ResultCh[T] {
	resultsCh := make(chan ResultCh[T])

	for _, fn := range fns {
		fn := fn

		go func(fn ExecuteFunc[T]) {
			result, err := fn(ctx)
			if err != nil {
				resultsCh <- ResultCh[T]{Output: result, Error: err}

				return
			}

			resultsCh <- ResultCh[T]{Output: result, Error: nil}
		}(fn)
	}

	return resultsCh
}
