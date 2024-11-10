// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thalesfsp/customerror"
	"github.com/thalesfsp/randomness"
	"github.com/thalesfsp/sypl"
	"github.com/thalesfsp/sypl/level"
	"golang.org/x/sync/semaphore"
)

// Map logger.
var mapLogger = sypl.NewDefault(Name, level.None).New("map")

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

type MapFuncCh[T any, Result any] func(ctx context.Context, item T, done chan<- struct{}) (Result, error)

//////
// Exported functionalities.
//////

// isZeroOfUnderlyingType checks if the value is the zero value.
func isZeroOfUnderlyingType(x interface{}) bool {
	if x == nil {
		return true
	}

	v := reflect.ValueOf(x)

	// Handle special cases.
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		if v.IsNil() {
			return true
		}
	}

	// Get the zero value for comparison.
	zero := reflect.Zero(v.Type()).Interface()

	return reflect.DeepEqual(x, zero)
}

// RemoveZeroValues removes zero values from the results.
func RemoveZeroValues[T any](removeZeroValues bool, results []T) []T {
	if !removeZeroValues || results == nil {
		return results
	}

	filtered := make([]T, 0, len(results))

	for _, item := range results {
		if !isZeroOfUnderlyingType(item) {
			filtered = append(filtered, item)
		}
	}

	return filtered
}

// Map concurrently applies a function `f` to each element in the slice `items`
// and returns the resulting slice and any errors that occurred. `f` should be of
// type MapFunc.
//
//nolint:funlen,gomnd,gocognit,mnd,gosec,wsl
func Map[T any, Result any](
	ctx context.Context,
	items []T,
	f MapFunc[T, Result],
	opts ...Func,
) ([]Result, Errors) {
	// Input Validation
	if items == nil {
		return nil, []error{errors.New("items cannot be nil")}
	}

	if f == nil {
		return nil, []error{errors.New("mapping function cannot be nil")}
	}

	if len(items) == 0 {
		return []Result{}, nil
	}

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

		resMutex sync.Mutex

		resultTracker uint64 = 1
	)

	var rdmn *randomness.Randomness

	if o.RandomDelayTimeMin != 0 || o.RandomDelayTimeMax != 0 &&
		o.RandomDelayTimeMin < o.RandomDelayTimeMax &&
		o.RandomDelayTimeDuration != 0 {
		r, err := randomness.New(o.RandomDelayTimeMin, o.RandomDelayTimeMax, 3, false)
		if err != nil {
			return nil, []error{err}
		}

		rdmn = r
	}

	for index := range items {
		select {
		case <-ctx.Done():
			// Context canceled, stop launching new goroutines
			break
		default:
			// Proceed with launching goroutine
		}

		// Randomness handling.
		if rdmn != nil {
			n, err := rdmn.Generate()
			if err != nil {
				return nil, []error{err}
			}

			dS := time.Duration(n) * o.RandomDelayTimeDuration

			mapLogger.Tracelnf("go routine %d is waiting for %v", index, dS)

			time.Sleep(dS)
		}

		// Limit handling.
		if o.Limit > 0 {
			if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
				break
			}
		}

		// Context error handling.
		if ctx.Err() != nil {
			errMutex.Lock()
			defer errMutex.Unlock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%+v"`, items[index])))

			// When returning results, use the fixed RemoveZeroValues
			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		}

		// Semaphore handling.
		if err := sem.Acquire(ctx, 1); err != nil {
			errMutex.Lock()
			defer errMutex.Unlock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%+v"`, items[index])))

			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		}

		//////
		// Loop of items.
		//////

		wg.Add(1)

		go func(index int) {
			defer sem.Release(1)
			defer wg.Done()

			select {
			case <-ctx.Done():
				// Context canceled, exit early
				return
			default:
				// Proceed with processing
			}

			mapLogger.Tracelnf("go routine %d started", index)

			res, err := f(ctx, items[index])
			if err != nil {
				errMutex.Lock()
				defer errMutex.Unlock()

				errs = append(errs, customerror.New(
					fmt.Sprintf("failed mapping, on item %+v", items[index]),
					customerror.WithError(err),
					customerror.WithTag(Name),
				))

				return
			}

			// Check if result i exists
			if len(results) <= index {
				errMutex.Lock()
				defer errMutex.Unlock()

				errs = append(errs, customerror.New(
					fmt.Sprintf("failed mapping, on item %+v", items[index]),
					customerror.WithError(fmt.Errorf("result index %v out of range", index)),
					customerror.WithTag(Name),
				))

				return
			}

			// Check limit.
			if o.Limit > 0 {
				if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
					return
				}
			}

			resMutex.Lock()
			results[index] = res
			resMutex.Unlock()

			atomic.AddUint64(&resultTracker, 1)
		}(index)
	}

	wg.Wait()

	// Check if context was canceled and prioritize its error
	if ctx.Err() != nil {
		errMutex.Lock()
		errs = append([]error{ctx.Err()}, errs...)
		errMutex.Unlock()
	}

	if len(errs) > 0 {
		return RemoveZeroValues(o.RemoveZeroValues, results), errs
	}

	return RemoveZeroValues(o.RemoveZeroValues, results), nil
}

// MapM concurrently applies a function `f` to each element in the map `itemMaps`
// and returns the resulting slice and any errors that occurred. `f` should be
// of type MapMFunc.
//
//nolint:funlen,gomnd,gocognit,mnd,gosec,wsl
func MapM[T any, Result any](
	ctx context.Context,
	itemsMap map[string]T,
	f MapMFunc[T, Result],
	opts ...Func,
) ([]Result, Errors) {
	// Input Validation
	if itemsMap == nil {
		return nil, []error{errors.New("itemsMap cannot be nil")}
	}

	if f == nil {
		return nil, []error{errors.New("mapping function cannot be nil")}
	}

	if len(itemsMap) == 0 {
		return []Result{}, nil
	}

	o := Option{
		BatchSize:        runtime.NumCPU(),
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

		resMutex sync.Mutex

		resultTracker uint64 = 1
	)

	var rdmn *randomness.Randomness

	if (o.RandomDelayTimeMin != 0 || o.RandomDelayTimeMax != 0) &&
		o.RandomDelayTimeMin < o.RandomDelayTimeMax &&
		o.RandomDelayTimeDuration != 0 {
		r, err := randomness.New(o.RandomDelayTimeMin, o.RandomDelayTimeMax, 3, false)
		if err != nil {
			return nil, []error{err}
		}

		rdmn = r
	}

	for key, item := range itemsMap {
		select {
		case <-ctx.Done():
			// Context canceled, stop launching new goroutines
			break
		default:
			// Proceed with launching goroutine
		}

		// Randomness handling.
		if rdmn != nil {
			n, err := rdmn.Generate()
			if err != nil {
				return nil, []error{err}
			}

			dS := time.Duration(n) * o.RandomDelayTimeDuration

			mapLogger.Tracelnf("go routine is waiting for key %s for %v", key, dS)

			time.Sleep(dS)
		}

		// Limit handling.
		if o.Limit > 0 {
			if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
				break
			}
		}

		// Context error handling.
		if ctx.Err() != nil {
			errMutex.Lock()
			defer errMutex.Unlock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%+v"`, key)))

			break
		}

		// Semaphore handling.
		if err := sem.Acquire(ctx, 1); err != nil {
			errMutex.Lock()
			defer errMutex.Unlock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%+v"`, key)))

			break
		}

		//////
		// Loop of items.
		//////

		wg.Add(1)

		go func(k string, it T) {
			defer sem.Release(1)
			defer wg.Done()

			select {
			case <-ctx.Done():
				// Context canceled, exit early
				return
			default:
				// Proceed with processing
			}

			mapLogger.Tracelnf("go routine started, key %s", k)

			res, err := f(ctx, k, it)
			if err != nil {
				errMutex.Lock()
				defer errMutex.Unlock()

				errs = append(errs, customerror.New(
					fmt.Sprintf("failed mapping, key %+v", k),
					customerror.WithTag(Name),
					customerror.WithError(err),
				))

				return
			}

			// Check limit.
			if o.Limit > 0 {
				if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
					return
				}
			}

			resMutex.Lock()
			results = append(results, res)
			resMutex.Unlock()

			atomic.AddUint64(&resultTracker, 1)
		}(key, item)
	}

	wg.Wait()

	// Check if context was canceled and prioritize its error
	if ctx.Err() != nil {
		errMutex.Lock()
		errs = append([]error{ctx.Err()}, errs...)
		errMutex.Unlock()
	}

	if len(errs) > 0 {
		return RemoveZeroValues(o.RemoveZeroValues, results), errs
	}

	return RemoveZeroValues(o.RemoveZeroValues, results), nil
}

// MapDone concurrently applies a function `f` to each element in the slice `items`
// and returns the resulting slice and any errors that occurred. `f` should be of
// type MapFunc. It also takes a channel `done` to signal early termination.
//
// The function takes an optional number of `Func` options that allow you to
// customize the behavior of the function.
//
// If an error occurs during execution of `f`, it is stored and returned along
// with the results. The order of the results matches the order of the input
// slice.
//
// If any of the operations are cancelled by the context or through the cancelCh,
// the function will return immediately.
// MapDone is a generic concurrent mapping function that processes slices of items in parallel
// while providing control over concurrency, delays, and result handling.
//
// Flow of the function:
// 1. Initialize options and concurrency controls
// 2. Set up error handling and result tracking
// 3. Configure random delay if specified
// 4. For each input item:
//   - Check for early termination signals
//   - Apply random delay if configured
//   - Check processing limits
//   - Acquire semaphore slot
//   - Launch goroutine to process item:
//   - Process item with provided function
//   - Handle errors
//   - Store results
//   - Update counters
//
// 5. Wait for all processing to complete
// 6. Return results and any errors
//
// Key Concepts:
// - Generics: The function uses type parameters (T, Result) to work with any data types
// - Concurrency: Uses goroutines for parallel processing
// - Synchronization:
//   - semaphore: Limits number of concurrent goroutines
//   - WaitGroup: Tracks completion of all goroutines
//   - Mutex: Protects shared resources (error slice)
//
// - Context: Handles cancellation and timeouts
// - Atomic Operations: Thread-safe counting of processed results
// - Channels: Used for signaling early termination
// - Error Handling: Collects and returns errors from all goroutines
//
// Type Parameters:
//   - T: The input type of items to be processed
//   - Result: The output type after processing each item
//
// Parameters:
//   - ctx: Context for cancellation and timeout control
//   - items: Slice of input items to be processed
//   - f: Function that processes each item and returns a Result
//   - opts: Optional configuration functions to modify default behavior
//
// Returns:
//   - []Result: Slice of processed results
//   - Errors: Any errors encountered during processing
//
//nolint:funlen,gomnd,gocognit,mnd,gosec
func MapDone[T any, Result any](
	ctx context.Context,
	items []T,
	f MapFuncCh[T, Result],
	opts ...Func,
) ([]Result, Errors) {
	// Input Validation
	if items == nil {
		return nil, []error{errors.New("items cannot be nil")}
	}

	if f == nil {
		return nil, []error{errors.New("mapping function cannot be nil")}
	}

	if len(items) == 0 {
		return []Result{}, nil
	}

	// Initialize default options
	o := Option{
		BatchSize:        runtime.NumCPU(), // Default to number of CPU cores
		RemoveZeroValues: true,             // Default to removing zero values from results
	}

	// Apply any provided option functions
	for _, opt := range opts {
		o = opt(o)
	}

	// Initialize concurrency control mechanisms
	sem := semaphore.NewWeighted(int64(o.BatchSize)) // Semaphore to limit concurrent goroutines
	wg := &sync.WaitGroup{}                          // WaitGroup to track completion of all goroutines
	results := make([]Result, len(items))            // Pre-allocate results slice

	// Initialize error handling
	var (
		errs     []error    // Slice to store errors
		errMutex sync.Mutex // Mutex to protect error slice access

		resMutex sync.Mutex

		resultTracker uint64 = 1 // Atomic counter for tracking processed results
	)

	// Initialize randomness generator for delay if configured
	var rdmn *randomness.Randomness

	if o.RandomDelayTimeMin != 0 ||
		o.RandomDelayTimeMax != 0 &&
			o.RandomDelayTimeMin < o.RandomDelayTimeMax &&
			o.RandomDelayTimeDuration != 0 {
		r, err := randomness.New(o.RandomDelayTimeMin, o.RandomDelayTimeMax, 3, false)
		if err != nil {
			return nil, []error{err}
		}

		rdmn = r
	}

	// Channel to signal early termination
	done := make(chan struct{})

	// Process each item in the input slice
	for index := range items {
		select {
		case <-done: // Check if processing should terminate early
			resMutex.Lock()
			defer resMutex.Unlock()

			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		case <-ctx.Done(): // Check if context has been cancelled
			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%+v"`, items[index])))

			resMutex.Lock()
			defer resMutex.Unlock()

			return RemoveZeroValues(o.RemoveZeroValues, results), errs
		default:
			// Apply random delay if configured
			if rdmn != nil {
				n, err := rdmn.Generate()
				if err != nil {
					return nil, []error{err}
				}

				dS := time.Duration(n) * o.RandomDelayTimeDuration

				mapLogger.Tracelnf("go routine %d is waiting for %v", index, dS)

				time.Sleep(dS)
			}

			// Check if we've hit the processing limit
			if o.Limit > 0 {
				if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
					break
				}
			}

			// Acquire semaphore slot (blocks if at capacity)
			if err := sem.Acquire(ctx, 1); err != nil {
				errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%+v"`, items[index])))

				resMutex.Lock()
				defer resMutex.Unlock()

				return RemoveZeroValues(o.RemoveZeroValues, results), errs
			}

			wg.Add(1) // Register new goroutine

			// Launch goroutine to process item
			go func(index int) {
				defer sem.Release(1) // Release semaphore slot when done
				defer wg.Done()      // Mark goroutine as complete

				mapLogger.Tracelnf("go routine %d started", index)

				// Process the item using provided function
				res, err := f(ctx, items[index], done)
				if err != nil {
					errMutex.Lock()
					errs = append(errs, customerror.New(
						fmt.Sprintf("failed mapping, on item %+v", items[index]),
						customerror.WithError(err),
						customerror.WithTag(Name),
					))
					errMutex.Unlock()

					return
				}

				// Validate result index
				if len(results) <= index {
					errMutex.Lock()
					errs = append(errs, customerror.New(
						fmt.Sprintf("failed mapping, on item %+v", items[index]),
						customerror.WithError(fmt.Errorf("result index %v out of range", index)),
						customerror.WithTag(Name),
					))
					errMutex.Unlock()

					return
				}

				// Check processing limit again
				if o.Limit > 0 {
					if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
						return
					}
				}

				// Store result and increment counter
				resMutex.Lock()
				results[index] = res
				resMutex.Unlock()

				atomic.AddUint64(&resultTracker, 1)
			}(index)
		}
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Return results and any errors
	if len(errs) > 0 {
		resMutex.Lock()
		defer resMutex.Unlock()

		return RemoveZeroValues(o.RemoveZeroValues, results), errs
	}

	resMutex.Lock()
	defer resMutex.Unlock()

	return RemoveZeroValues(o.RemoveZeroValues, results), nil
}
