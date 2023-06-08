// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/thalesfsp/customerror"
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

// isZeroOfUnderlyingType checks if the value is the zero value.
func isZeroOfUnderlyingType(x interface{}) bool {
	return reflect.DeepEqual(x, reflect.Zero(reflect.TypeOf(x)).Interface())
}

// RemoveZeroValues removes zero values from the results.
func RemoveZeroValues[T any](removeZeroValues bool, results []T) []T {
	if removeZeroValues {
		for i := 0; i < len(results); i++ {
			if isZeroOfUnderlyingType(results[i]) {
				results = append(results[:i], results[i+1:]...)
				i--
			}
		}
	}

	return results
}

// Map concurrently applies a function `f` to each element in the slice `sl` and
// returns the resulting slice and any errors that occurred. `f` should be of
// type  MapFunc, a function which takes a context and an element of type `T`
// and  returns a result of type `Result` and an error.
//
// The function takes an optional number of `Func` options that allow you to
// customize the behavior of the function.
//
// If an error occurs during execution of `f`, it is stored and returned along
// with the results. The order of the results matches the order of the input
// slice.
//
// If any of the operations are cancelled by the context, the function will
// panic.
//
// Usage example:
//
//	type MyStruct struct { ... }
//
//	func process(ctx context.Context, s MyStruct) (ResultType, error) { ... }
//
//	sl := []MyStruct{...}
//	ctx := context.Background()
//	results, errs := Map(ctx, sl, process)
//
//	if errs != nil {
//	    // handle errors
//	}
//
//	// process results
//
// Note: Because the function executes concurrently, the functions you provide must
// be safe for concurrent use.
func Map[T any, Result any](
	ctx context.Context,
	sl []T,
	f MapFunc[T, Result],
	opts ...Func,
) ([]Result, Errors) {
	o := Option{
		Concurrency:      runtime.GOMAXPROCS(0),
		RemoveZeroValues: true,
	}

	for _, opt := range opts {
		o = opt(o)
	}

	sem := semaphore.NewWeighted(int64(o.Concurrency))

	wg := &sync.WaitGroup{}

	results := make([]Result, len(sl))

	var (
		errs     []error
		errMutex sync.Mutex

		resultTracker uint64 = 1
	)

	for i := range sl {
		if o.Limit > 0 {
			if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
				break
			}
		}

		if ctx.Err() != nil {
			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%v"`, sl[i])))

			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		}

		if err := sem.Acquire(ctx, 1); err != nil {
			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%v"`, sl[i])))

			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		}

		wg.Add(1)

		go func(i int) {
			defer sem.Release(1)
			defer wg.Done()

			res, err := f(ctx, sl[i])
			if err != nil {
				errMutex.Lock()
				defer errMutex.Unlock()

				errs = append(errs, customerror.New(
					fmt.Sprintf("failed to map %v", sl[i]),
					customerror.WithError(err),
				))

				return
			}

			// Check if result i exists
			if len(results) <= i {
				errMutex.Lock()
				defer errMutex.Unlock()

				errs = append(errs, customerror.New(
					fmt.Sprintf("failed to map %v", sl[i]),
					customerror.WithError(fmt.Errorf("result index %v out of range", i)),
				))

				return
			}

			// resMutex.Lock()
			// defer resMutex.Unlock()

			if o.Limit > 0 {
				if resultTracker > uint64(o.Limit) {
					return
				}
			}

			results[i] = res

			atomic.AddUint64(&resultTracker, 1)
		}(i)
	}

	wg.Wait()

	if len(errs) > 0 {
		return RemoveZeroValues(o.RemoveZeroValues, results), errs
	}

	return RemoveZeroValues(o.RemoveZeroValues, results), nil
}
