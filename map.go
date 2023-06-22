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
	"time"

	"github.com/thalesfsp/customerror"
	"github.com/thalesfsp/randomness"
	"golang.org/x/sync/semaphore"
)

//////
// Vars, consts, and types.
//////

// MapFunc is the type of the function that will be executed concurrently for each
// element in a slice of type `T`. The function takes a `context.Context` and a
// value of type `T`, and returns a value of type `Result` and an error value.
type MapFunc[T any, Result any] func(ctx context.Context, item T) (Result, error)

// MapMFunc is the type of the function that will be executed concurrently for
// each element in the map.
type MapMFunc[T any, Result any] func(ctx context.Context, key string, item T) (Result, error)

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

// Map concurrently applies a function `f` to each element in the slice `items`
// and returns the resulting slice and any errors that occurred. `f` should be of
// type MapFunc.
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
//	items := []MyStruct{...}
//	ctx := context.Background()
//	results, errs := Map(ctx, items, process)
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
	items []T,
	f MapFunc[T, Result],
	opts ...Func,
) ([]Result, Errors) {
	o := Option{
		BatchSize:        runtime.NumCPU(),
		RemoveZeroValues: true,
	}

	for _, opt := range opts {
		o = opt(o)
	}

	sem := semaphore.NewWeighted(int64(o.BatchSize))

	wg := &sync.WaitGroup{}

	results := make([]Result, len(items))

	var (
		errs     []error
		errMutex sync.Mutex

		resultTracker uint64 = 1
	)

	var rN int64

	if o.RandomDelayTimeMin != 0 || o.RandomDelayTimeMax != 0 && o.RandomDelayTimeMin < o.RandomDelayTimeMax && o.RandomDelayTimeDuration != 0 {
		r, err := randomness.New(o.RandomDelayTimeMin, o.RandomDelayTimeMax, 3, false)
		if err != nil {
			return nil, []error{err}
		}

		n, err := r.Generate()
		if err != nil {
			return nil, []error{err}
		}

		rN = n
	}

	for i := range items {
		if rN != 0 {
			time.Sleep(time.Duration(rN) * o.RandomDelayTimeDuration)
		}

		if o.Limit > 0 {
			if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
				break
			}
		}

		if ctx.Err() != nil {
			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%v"`, items[i])))

			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		}

		if err := sem.Acquire(ctx, 1); err != nil {
			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%v"`, items[i])))

			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		}

		wg.Add(1)

		go func(i int) {
			defer sem.Release(1)
			defer wg.Done()

			res, err := f(ctx, items[i])
			if err != nil {
				errMutex.Lock()
				defer errMutex.Unlock()

				errs = append(errs, customerror.New(
					fmt.Sprintf("failed to map %v", items[i]),
					customerror.WithError(err),
				))

				return
			}

			// Check if result i exists
			if len(results) <= i {
				errMutex.Lock()
				defer errMutex.Unlock()

				errs = append(errs, customerror.New(
					fmt.Sprintf("failed to map %v", items[i]),
					customerror.WithError(fmt.Errorf("result index %v out of range", i)),
				))

				return
			}

			// resMutex.Lock()
			// defer resMutex.Unlock()

			if o.Limit > 0 {
				if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
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

// MapM concurrently applies a function `f` to each element in the map `itemMaps`
// and returns the resulting slice and any errors that occurred. `f` should be
// of type MapMFunc.
func MapM[T any, Result any](
	ctx context.Context,
	itemsMap map[string]T,
	f MapMFunc[T, Result],
	opts ...Func,
) ([]Result, Errors) {
	o := Option{
		BatchSize:        runtime.GOMAXPROCS(0),
		RemoveZeroValues: true,
	}

	for _, opt := range opts {
		o = opt(o)
	}

	sem := semaphore.NewWeighted(int64(o.BatchSize))

	wg := &sync.WaitGroup{}

	results := []Result{}

	var (
		errs     []error
		errMutex sync.Mutex

		resultTracker uint64 = 1
	)

	for key, item := range itemsMap {
		// Limit handling.
		if o.Limit > 0 {
			if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
				break
			}
		}

		// Context error handling.
		if ctx.Err() != nil {
			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%v"`, key)))

			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		}

		// Semaphore handling.
		if err := sem.Acquire(ctx, 1); err != nil {
			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%v"`, key)))

			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		}

		//////
		// Loop of items.
		//////

		wg.Add(1)

		go func(k string, i T) {
			defer sem.Release(1)
			defer wg.Done()

			res, err := f(ctx, k, i)
			if err != nil {
				errMutex.Lock()
				defer errMutex.Unlock()

				errs = append(errs, customerror.New(
					fmt.Sprintf("failed to map %v", k),
					customerror.WithError(err),
				))

				return
			}

			// Limit feature.
			if o.Limit > 0 {
				if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
					return
				}
			}

			errMutex.Lock()
			defer errMutex.Unlock()

			results = append(results, res)

			atomic.AddUint64(&resultTracker, 1)
		}(key, item)
	}

	wg.Wait()

	if len(errs) > 0 {
		return RemoveZeroValues(o.RemoveZeroValues, results), errs
	}

	return RemoveZeroValues(o.RemoveZeroValues, results), nil
}
