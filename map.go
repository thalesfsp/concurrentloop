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

// MapFunc is the type of the function that will be executed concurrently for each
// element in a slice of type `T`. The function takes a `context.Context` and a
// value of type `T`, and returns a value of type `Result` and an error value.
type MapFunc[T any, Result any] func(context.Context, T) (Result, error)

//////
// Exported functionalities.
//////

// Map calls the `Func` concurrently on each element of `sl`, and returns the
// results and any errors that occurred. The function blocks until all
// executions have completed.
//
// NOTE: Order is preserved.
func Map[T any, Result any](ctx context.Context, sl []T, f MapFunc[T, Result]) ([]Result, Errors) {
	// Calls MapCh, and closes the channel.
	resultsCh := MapCh(ctx, sl, f)
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

// MapCh calls the `Func` concurrently on each element of `sl`, and returns a
// channel that receives the results. The results are returned as an `ResultCh`
// struct, which contains the output value and an error value if the function
// call failed.
//
// NOTE: It's the caller's responsibility to close the channel.
func MapCh[T any, Result any](ctx context.Context, sl []T, f MapFunc[T, Result]) chan ResultCh[Result] {
	resultsCh := make(chan ResultCh[Result])

	for i, t := range sl {
		t := t
		i := i

		go func(i int, t T) {
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
