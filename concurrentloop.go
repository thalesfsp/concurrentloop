// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

import (
	"context"
	"strings"
)

// Errors is a slice of errors.
type Errors []error

// Error returns a string representation of the combined errors in the `Errors`
// slice, separated by commas. This method satisfies the `error` interface.
//
//nolint:prealloc
func (e Errors) Error() string {
	var errs []string

	for _, err := range e {
		errs = append(errs, err.Error())
	}

	return strings.Join(errs, ", ")
}

// ResultCh receives the result from the channel.
type ResultCh[T any] struct {
	Output T
	Error  error
}

// Func is the type of the function that will be executed concurrently for each
// element in a slice of type `T`. The function takes a `context.Context` and a
// value of type `T`, and returns a value of type `Result` and an error value.
//
// Example usage:
//
//	func myFunc(ctx context.Context, s string) (int, error) {
//	    // Perform some computation using the input `s`.
//	    ...
//	    return result, err
//	}
//
//	var sl []string
//	// Add strings to `sl`.
//	...
//
//	results, errs := Run(ctx, sl, myFunc)
type Func[T any, Result any] func(context.Context, T) (Result, error)

// Run calls the `Func` concurrently on each element of `sl`, and returns the
// results and any errors that occurred. The function blocks until all
// executions have completed.
//
// Example usage:
//
//	results, errs := Run(ctx, sl, f)
//
//	// Process any errors that were returned.
//	if len(errs) > 0 {
//	    // Handle errors.
//	}
//
//	// Process the results.
//	// ...
func Run[T any, Result any](ctx context.Context, sl []T, f Func[T, Result]) ([]Result, Errors) {
	// Calls runCh, and closes the channel.
	resultsCh := RunCh(ctx, sl, f)
	defer close(resultsCh)

	var (
		results []Result
		errs    []error
	)

	for range sl {
		result := <-resultsCh

		if result.Error != nil {
			errs = append(errs, result.Error)
		} else {
			results = append(results, result.Output)
		}
	}

	return results, errs
}

// RunCh calls the `Func` concurrently on each element of `sl`, and returns a
// channel that receives the results. The results are returned as a `resultCh`
// struct, which contains the output value and an error value if the function
// call failed.
//
// Example usage:
//
//	resultsCh := RunCh(ctx, sl, f)
//	defer close(resultsCh)
//
//	var (
//	    results []Result
//	    errs    []error
//	)
//
//	for range sl {
//	    result := <-resultsCh
//
//	    if result.Error != nil {
//	        errs = append(errs, result.Error)
//	    } else {
//	        results = append(results, result.Output)
//	    }
//	}
//
//	// Process any errors that were returned.
//	if len(errs) > 0 {
//	    // Handle errors.
//	}
//
//	// Process the results.
//	// ...
//
// NOTE: It's the caller's responsibility to close the channel.
func RunCh[T any, Result any](ctx context.Context, sl []T, f Func[T, Result]) chan ResultCh[Result] {
	// Create a channel to receive the results.
	resultsCh := make(chan ResultCh[Result])

	// Concurrently call, and send the result to the channel.
	for _, t := range sl {
		t := t

		go func(sl []T) {
			result, err := f(ctx, t)
			resultsCh <- ResultCh[Result]{Output: result, Error: err}
		}(sl)
	}

	return resultsCh
}
