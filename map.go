// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

import (
	"context"
	"encoding/json"
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
// Vars, consts, and types.
//////

// MapFunc is the type of the function that will be executed concurrently for each
// element in a slice of type `T`. The function takes a `context.Context` and a
// value of type `T`, and returns a value of type `Result` and an error value.
type MapFunc[T any, Result any] func(ctx context.Context, item T) (Result, error)

// MapMFunc is the type of the function that will be executed concurrently for
// each element in the map.
type MapMFunc[T any, Result any] func(ctx context.Context, key string, item T) (Result, error)

// MapFuncCh is the type of the function that will be executed concurrently for each
// element in a slice with a done channel for early termination.
type MapFuncCh[T any, Result any] func(ctx context.Context, item T, done chan<- struct{}) (Result, error)

// MapMFuncCh is the type of the function that will be executed concurrently for each
// element in a map with per-cycle and end channels for real-time result streaming.
type MapMFuncCh[T any, Result any] func(
	ctx context.Context,
	key string,
	item T,
	perCycleCh chan<- Result,
	endCh chan<- Result,
) (Result, error)

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

// Map processes each element in a slice concurrently using the provided function.
// Returns a slice of results and any errors that occurred during processing.
//
//nolint:funlen,gomnd,gocognit,mnd
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

indexLoop:
	for index := range items {
		select {
		case <-ctx.Done():
			// Context canceled, stop launching new goroutines.
			break indexLoop
		default:
			// Proceed with launching goroutine.
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
				break indexLoop
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
				// Context canceled, exit early.
				return
			default:
				// Proceed with processing.
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

			// Check if result i exists.
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

			// Write to writer if specified.
			if o.Writer != nil {
				if resBytes, err := json.Marshal(res); err == nil {
					if _, writeErr := o.Writer.Write(append(resBytes, '\n')); writeErr != nil {
						errMutex.Lock()

						errs = append(errs, customerror.New(
							"failed to write result to writer",
							customerror.WithError(writeErr),
							customerror.WithTag(Name),
						))

						errMutex.Unlock()
						resMutex.Unlock()

						return
					}
				}
			}

			resMutex.Unlock()

			atomic.AddUint64(&resultTracker, 1)
		}(index)
	}

	// Wait for all worker goroutines OR the caller's context to fire,
	// whichever comes first. See waitForWaitGroupOrCtx godoc below for
	// the full rationale — without it, a worker stuck inside a leaked
	// downstream goroutine (e.g. thalesfsp/ebi.BulkCreate's metrics
	// loop) would block wg.Wait forever, holding the caller hostage
	// past ctx.Err(). The 2026-05-01 proj-ringboost-vendor v195→v196
	// rotation hung for 2+ hours via this exact path. Regression test:
	// TestMap_StuckChild_CtxFires_ReturnsWithinBound.
	if err := waitForWaitGroupOrCtx(ctx, wg); err != nil {
		// Snapshot results AND errs under their respective mutexes so
		// a late-acquiring worker (still running because waitForWait
		// GroupOrCtx returned via the ctx branch, not the wg branch)
		// cannot race with the caller reading the returned slices.
		//
		// v1.4.3 snapshotted results but NOT errs — caught by Grok
		// review on UVS v2.0.118. errs uses append() which can
		// reallocate the backing array; the parent's returned slice
		// header would point at the pre-append memory while a late
		// worker's append produced a new array, leaving the parent's
		// reads racing the worker's writes against the original
		// (potentially recycled) array.
		//
		// Both snapshots are taken under their own locks; the locks
		// are independent so we acquire+release each in turn rather
		// than holding both simultaneously.
		resMutex.Lock()
		snapshot := make([]Result, len(results))
		copy(snapshot, results)
		resMutex.Unlock()

		errMutex.Lock()
		errs = append([]error{err}, errs...)
		errsSnapshot := make([]error, len(errs))
		copy(errsSnapshot, errs)
		errMutex.Unlock()

		return RemoveZeroValues(o.RemoveZeroValues, snapshot), errsSnapshot
	}

	// Check if context was canceled and prioritize its error.
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

// waitForWaitGroupOrCtx blocks until either wg.Wait() returns OR ctx fires
// Done(). Returns ctx.Err() if ctx fired first, nil if all workers finished.
//
// This is the safety net that ensures Map/MapM cannot hang indefinitely
// when a worker goroutine fails to call wg.Done() — for example because
// it is blocked on a leaked goroutine in a downstream library that does
// not honor context cancellation. Without this, a single misbehaving
// child can hold every concurrent caller hostage forever.
//
// The "waiter" goroutine spawned here is bounded: it returns as soon as
// wg.Wait() returns, regardless of whether the parent select picked the
// ctx branch. So no extra goroutine leak is introduced by this helper —
// see TestMap_NoGoroutineLeakFromHelper for the regression guard.
func waitForWaitGroupOrCtx(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// MapM processes each key-value pair in a map concurrently using the provided function.
// Returns a slice of results and any errors that occurred during processing.
//
//nolint:funlen,gomnd,gocognit,mnd
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

keyLoop:
	for key, item := range itemsMap {
		select {
		case <-ctx.Done():
			// Context canceled, stop launching new goroutines.
			break keyLoop
		default:
			// Proceed with launching goroutine.
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
				break keyLoop
			}
		}

		// Context error handling.
		if ctx.Err() != nil {
			errMutex.Lock()
			defer errMutex.Unlock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%+v"`, key)))

			break keyLoop
		}

		// Semaphore handling.
		if err := sem.Acquire(ctx, 1); err != nil {
			errMutex.Lock()
			defer errMutex.Unlock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%+v"`, key)))

			break keyLoop
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

			// Write to writer if specified.
			if o.Writer != nil {
				if resBytes, err := json.Marshal(res); err == nil {
					if _, writeErr := o.Writer.Write(append(resBytes, '\n')); writeErr != nil {
						errMutex.Lock()

						errs = append(errs, customerror.New(
							"failed to write result to writer",
							customerror.WithError(writeErr),
							customerror.WithTag(Name),
						))

						errMutex.Unlock()
						resMutex.Unlock()

						return
					}
				}
			}

			resMutex.Unlock()

			atomic.AddUint64(&resultTracker, 1)
		}(key, item)
	}

	// See waitForWaitGroupOrCtx godoc above (used by Map). Same rationale
	// here: a worker goroutine blocked on a leaked downstream goroutine
	// must NOT hold the caller hostage past ctx.Err(). Snapshot results
	// AND errs under their respective mutexes on early return to keep
	// concurrent late-writers race-free against the caller. Regression
	// tests: TestMapM_StuckChild_CtxFires_ReturnsWithinBound (timing)
	// and TestMapM_LateWorker_ErrsSliceRaceFree (race).
	//
	// v1.4.4 fix: errs is now snapshotted symmetrically with results.
	// v1.4.3 only snapshotted results, leaving the errs slice header
	// racy when a late-acquiring worker appends after the parent's
	// errMutex.Unlock — caught by Grok 4.20 review on UVS v2.0.118.
	if err := waitForWaitGroupOrCtx(ctx, wg); err != nil {
		resMutex.Lock()
		snapshot := make([]Result, len(results))
		copy(snapshot, results)
		resMutex.Unlock()

		errMutex.Lock()
		errs = append([]error{err}, errs...)
		errsSnapshot := make([]error, len(errs))
		copy(errsSnapshot, errs)
		errMutex.Unlock()

		return RemoveZeroValues(o.RemoveZeroValues, snapshot), errsSnapshot
	}

	// Write to writer as JSON array if specified.
	if o.Writer != nil {
		finalResults := RemoveZeroValues(o.RemoveZeroValues, results)
		if resultsBytes, err := json.Marshal(finalResults); err == nil {
			if _, writeErr := o.Writer.Write(resultsBytes); writeErr != nil {
				errMutex.Lock()
				errs = append(errs, customerror.New(
					"failed to write final results to writer",
					customerror.WithError(writeErr),
					customerror.WithTag(Name),
				))
				errMutex.Unlock()
			}
		}
	}

	// Check if context was canceled and prioritize its error.
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

// MapDone processes each element in a slice concurrently with early termination support.
// The processing function receives a done channel that can be used to signal early termination.
// Returns a slice of results and any errors that occurred during processing.
//
//nolint:gomnd,gocognit,mnd
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

// MapCh processes each key-value pair in a map concurrently with real-time result streaming.
// It follows the same pattern as Map but sends results to channels instead of returning them.
// Per-cycle results are sent to perCycleCh, final results (after RemoveZeroValues) are sent to endCh.
// Returns only errors that occurred during processing.
//
//nolint:funlen,gomnd,gocognit,mnd,gocyclo,maintidx
func MapCh[T any, Result any](
	ctx context.Context,
	itemsMap map[string]T,
	f MapMFuncCh[T, Result],
	perCycleCh chan<- Result,
	endCh chan<- Result,
	opts ...Func,
) Errors {
	// Input Validation
	if itemsMap == nil {
		return []error{errors.New("itemsMap cannot be nil")}
	}

	if f == nil {
		return []error{errors.New("mapping function cannot be nil")}
	}

	// At least one channel must be provided for result streaming
	if perCycleCh == nil && endCh == nil {
		return []error{errors.New("at least one channel (perCycleCh or endCh) must be provided")}
	}

	if len(itemsMap) == 0 {
		return nil
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

	// Collect results like Map does, but send them to channels instead of returning
	results := make([]Result, 0, len(itemsMap))
	resultKeys := make([]string, 0, len(itemsMap))

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
			return []error{err}
		}

		rdmn = r
	}

itemLoop:
	for key, item := range itemsMap {
		select {
		case <-ctx.Done():
			// Context canceled, stop launching new goroutines.
			break itemLoop
		default:
			// Proceed with launching goroutine.
		}

		// Randomness handling.
		if rdmn != nil {
			n, err := rdmn.Generate()
			if err != nil {
				return []error{err}
			}

			dS := time.Duration(n) * o.RandomDelayTimeDuration

			mapLogger.Tracelnf("go routine is waiting for key %s for %v", key, dS)

			time.Sleep(dS)
		}

		// Limit handling.
		if o.Limit > 0 {
			if atomic.LoadUint64(&resultTracker) > uint64(o.Limit) {
				break itemLoop
			}
		}

		// Context error handling.
		if ctx.Err() != nil {
			errMutex.Lock()
			defer errMutex.Unlock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%+v"`, key)))

			break itemLoop
		}

		// Semaphore handling.
		if err := sem.Acquire(ctx, 1); err != nil {
			errMutex.Lock()
			defer errMutex.Unlock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%+v"`, key)))

			break itemLoop
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
				// Context canceled, exit early.
				return
			default:
				// Proceed with processing.
			}

			mapLogger.Tracelnf("go routine started, key %s", k)

			res, err := f(ctx, k, it, perCycleCh, endCh)
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
			resultKeys = append(resultKeys, k)

			// NOTE: perCycleCh is handled by the mapping function f itself.
			// MapCh only collects results and sends final results to endCh.

			// Write to writer if specified.
			if o.Writer != nil {
				if resBytes, err := json.Marshal(res); err == nil {
					if _, writeErr := o.Writer.Write(append(resBytes, '\n')); writeErr != nil {
						errMutex.Lock()

						errs = append(errs, customerror.New(
							"failed to write result to writer",
							customerror.WithError(writeErr),
							customerror.WithTag(Name),
						))

						errMutex.Unlock()
						resMutex.Unlock()

						return
					}
				}
			}

			resMutex.Unlock()

			atomic.AddUint64(&resultTracker, 1)
		}(key, item)
	}

	wg.Wait()

	// Send final results to endCh after applying RemoveZeroValues.
	if endCh != nil {
		resMutex.Lock()
		finalResults := RemoveZeroValues(o.RemoveZeroValues, results)
	endChLoop:
		for _, result := range finalResults {
			select {
			case endCh <- result:
			case <-ctx.Done():
				break endChLoop
			}
		}
		resMutex.Unlock()
	}

	// Check if context was canceled and prioritize its error.
	if ctx.Err() != nil {
		errMutex.Lock()
		errs = append([]error{ctx.Err()}, errs...)
		errMutex.Unlock()
	}

	if len(errs) > 0 {
		return errs
	}

	return nil
}
