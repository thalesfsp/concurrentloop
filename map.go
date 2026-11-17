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
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/thalesfsp/customerror"
	"github.com/thalesfsp/randomness"
	"github.com/thalesfsp/sypl/v2"
	"github.com/thalesfsp/sypl/v2/level"
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
func isZeroOfUnderlyingType(x any) bool {
	if x == nil {
		return true
	}

	v := reflect.ValueOf(x)

	// Handle special cases.
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
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

func mapOptions(opts []Func) Option {
	var o Option

	o.BatchSize = runtime.NumCPU()

	o.RemoveZeroValues = true

	for _, opt := range opts {
		o = opt(o)
	}

	return o
}

// Map processes each element in a slice concurrently using the provided function.
// Returns a slice of results and any errors that occurred during processing.
//
//nolint:gomnd,gocognit,mnd
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

	o := mapOptions(opts)

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
		o.RandomDelayTimeMin < o.RandomDelayTimeMax && o.RandomDelayTimeDuration != 0 {
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

			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%+v"`, items[index])))
			errMutex.Unlock()

			break indexLoop
		}

		// Semaphore handling.
		err := sem.Acquire(ctx, 1)
		if err != nil {
			errMutex.Lock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%+v"`, items[index])))
			errMutex.Unlock()

			break indexLoop
		}

		//////
		// Loop of items.
		//////

		wg.Add(1)

		go func(index int) {
			defer sem.Release(1)
			defer wg.Done()

			mapWorker(ctx, index, items, f, o, results, &errs, &errMutex, &resMutex, &resultTracker)
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
	err := waitForWaitGroupOrCtx(ctx, wg)
	if err != nil {
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

// mapWorker runs the body of a single Map goroutine: it invokes f on
// items[index] and records the result (indexed write) or error under the
// appropriate mutex. Extracted verbatim from Map's per-item closure so the
// hot path stays within the gocyclo/maintidx budget without altering
// behaviour — sem.Release and wg.Done stay deferred in the launching
// closure. Mutexes and the shared errs/resultTracker are passed by
// pointer (never copy a sync.Mutex); results is the pre-allocated slice
// whose backing array is written in place.
func mapWorker[T any, Result any](
	ctx context.Context,
	index int,
	items []T,
	f MapFunc[T, Result],
	o Option,
	results []Result,
	errs *[]error,
	errMutex *sync.Mutex,
	resMutex *sync.Mutex,
	resultTracker *uint64,
) {
	select {
	case <-ctx.Done():
		// Context canceled, exit early.
		return
	default:
	}

	mapLogger.Tracelnf("go routine %d started", index)

	res, err := f(ctx, items[index])
	if err != nil {
		errMutex.Lock()
		defer errMutex.Unlock()

		*errs = append(*errs, customerror.New(
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

		*errs = append(*errs, customerror.New(
			fmt.Sprintf("failed mapping, on item %+v", items[index]),
			customerror.WithError(fmt.Errorf("result index %v out of range", index)),
			customerror.WithTag(Name),
		))

		return
	}

	// Check limit.
	if o.Limit > 0 {
		if atomic.LoadUint64(resultTracker) > uint64(o.Limit) {
			return
		}
	}

	resMutex.Lock()
	results[index] = res

	// Write to writer if specified.
	if o.Writer != nil {
		resBytes, err := json.Marshal(res)
		if err == nil {
			_, writeErr := o.Writer.Write(append(resBytes, '\n'))
			if writeErr != nil {
				errMutex.Lock()

				*errs = append(*errs, customerror.New(
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

	atomic.AddUint64(resultTracker, 1)
}

// waitForWaitGroupOrCtx blocks until wg.Wait returns or ctx is canceled.
// Returns ctx.Err() for cancellation, otherwise nil.
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
//nolint:gomnd,gocognit,mnd
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

	o := mapOptions(opts)

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
		//
		// v1.4.4 Grok-followup: explicit Unlock (NOT defer) before
		// `break keyLoop`. defer fires on FUNCTION return, not on
		// loop break — so a deferred Unlock here would leave errMutex
		// held when execution continues to the post-wait snapshot
		// block at line ~570 below, which calls errMutex.Lock again
		// on the SAME goroutine and self-deadlocks (Go mutexes are
		// not recursive). Hold the lock only for the append, then
		// release explicitly.
		if ctx.Err() != nil {
			errMutex.Lock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%+v"`, key)))
			errMutex.Unlock()

			break keyLoop
		}

		// Semaphore handling. Same explicit-Unlock discipline as the
		// ctx-err block above (v1.4.4 Grok-followup deadlock fix).
		err := sem.Acquire(ctx, 1)
		if err != nil {
			errMutex.Lock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%+v"`, key)))
			errMutex.Unlock()

			break keyLoop
		}

		//////
		// Loop of items.
		//////

		wg.Add(1)

		go func(k string, it T) {
			defer sem.Release(1)
			defer wg.Done()

			mapMWorker(ctx, k, it, f, o, &results, &errs, &errMutex, &resMutex, &resultTracker)
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
	err := waitForWaitGroupOrCtx(ctx, wg)
	if err != nil {
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

		resultsBytes, err := json.Marshal(finalResults)
		if err == nil {
			_, writeErr := o.Writer.Write(resultsBytes)
			if writeErr != nil {
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

	return RemoveZeroValues(o.RemoveZeroValues, results), errs
}

// mapMWorker runs the body of a single MapM goroutine: it invokes f on the
// key/item pair and records the result (appended) or error under the
// appropriate mutex. Extracted verbatim from MapM's per-key closure so the
// hot path stays within the gocyclo/maintidx budget without altering
// behaviour — sem.Release and wg.Done stay deferred in the launching
// closure. Mutexes, errs, resultTracker and the appended results slice are
// passed by pointer (never copy a sync.Mutex; results grows via append).
func mapMWorker[T any, Result any](
	ctx context.Context,
	key string,
	item T,
	f MapMFunc[T, Result],
	o Option,
	results *[]Result,
	errs *[]error,
	errMutex *sync.Mutex,
	resMutex *sync.Mutex,
	resultTracker *uint64,
) {
	select {
	case <-ctx.Done():
		// Context canceled, exit early
		return
	default:
	}

	mapLogger.Tracelnf("go routine started, key %s", key)

	res, err := f(ctx, key, item)
	if err != nil {
		errMutex.Lock()
		defer errMutex.Unlock()

		*errs = append(*errs, customerror.New(
			fmt.Sprintf("failed mapping, key %+v", key),
			customerror.WithTag(Name),
			customerror.WithError(err),
		))

		return
	}

	// Check limit.
	if o.Limit > 0 {
		if atomic.LoadUint64(resultTracker) > uint64(o.Limit) {
			return
		}
	}

	resMutex.Lock()

	*results = append(*results, res)

	// Write to writer if specified.
	if o.Writer != nil {
		resBytes, err := json.Marshal(res)
		if err == nil {
			_, writeErr := o.Writer.Write(append(resBytes, '\n'))
			if writeErr != nil {
				errMutex.Lock()

				*errs = append(*errs, customerror.New(
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

	atomic.AddUint64(resultTracker, 1)
}

// MapDone processes each element in a slice concurrently with early termination support.
// The processing function can send to or close done to request early termination.
// The library snapshots its result/error slices before returning; callbacks may finish later.
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
	o := mapOptions(opts)

	// Channel slots let acquisition observe both cancellation and done.
	slots := make(chan struct{}, max(0, o.BatchSize))
	completed := make(chan struct{}, len(items))
	launched := 0
	results := make([]Result, len(items))

	// Initialize error handling
	var (
		errs               []error
		errMutex, resMutex sync.Mutex
		resultTracker      uint64 = 1
	)

	// Initialize randomness generator for delay if configured
	var rdmn *randomness.Randomness

	if o.RandomDelayTimeMin != 0 || o.RandomDelayTimeMax != 0 &&
		o.RandomDelayTimeMin < o.RandomDelayTimeMax && o.RandomDelayTimeDuration != 0 {
		r, err := randomness.New(o.RandomDelayTimeMin, o.RandomDelayTimeMax, 3, false)
		if err != nil {
			return nil, []error{err}
		}

		rdmn = r
	}

	// Channel to signal early termination
	done := make(chan struct{})
	stopped := false

	// Process each item in the input slice
doneLoop:
	for index := range items {
		select {
		case <-done: // Check if processing should terminate early
			stopped = true

			break doneLoop
		case <-ctx.Done(): // Check if context has been cancelled
			errMutex.Lock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%+v"`, items[index])))
			errMutex.Unlock()

			break doneLoop
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

			// A single done send also interrupts a full batch.
			select {
			case <-done:
				stopped = true

				break doneLoop
			case <-ctx.Done():
				errMutex.Lock()

				errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%+v"`, items[index])))
				errMutex.Unlock()

				break doneLoop
			case slots <- struct{}{}:
			}

			launched++

			// Launch goroutine to process item
			go func(index int) {
				defer func() { <-slots }()
				defer func() { completed <- struct{}{} }()

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

	// Cancellation and done must remain responsive even if a callback ignores
	// them. Snapshot both slices: a late worker still owns the original state.
	var err error
	if !stopped {
		err = waitForCompletions(ctx, completed, done, launched)
	}

	return snapshotMapDone(err, o.RemoveZeroValues, results, &errs, &resMutex, &errMutex)
}

// waitForCompletions does not spawn a waiter: late callbacks send into the
// bounded completion buffer and can finish after cancellation or a done signal.
func waitForCompletions(ctx context.Context, completed, done <-chan struct{}, pending int) error {
	for pending > 0 {
		select {
		case <-completed:
			pending--
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return nil
		}
	}

	return nil
}

// snapshotMapDone copies state while late workers retain their original slices.
func snapshotMapDone[T any](
	err error, removeZero bool, results []T, errs *[]error, resMutex, errMutex *sync.Mutex,
) ([]T, Errors) {
	resMutex.Lock()
	defer resMutex.Unlock()

	errMutex.Lock()
	defer errMutex.Unlock()

	if err != nil {
		*errs = append([]error{err}, (*errs)...)
	}

	return RemoveZeroValues(removeZero, slices.Clone(results)), slices.Clone(Errors(*errs))
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

	o := mapOptions(opts)

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
		//
		// v1.4.4 Grok-followup: explicit Unlock (NOT defer) before
		// `break itemLoop` — same deadlock anti-pattern as MapM's
		// keyLoop. After the break, MapCh calls wg.Wait() to drain
		// workers; any worker that calls errMutex.Lock() in its own
		// err path (e.g. the writer-error block) blocks waiting for
		// the deferred Unlock that only fires on MapCh return — the
		// worker never completes, wg.Wait never returns, MapCh hangs.
		if ctx.Err() != nil {
			errMutex.Lock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context errored before mapping "%+v"`, key)))
			errMutex.Unlock()

			break itemLoop
		}

		// Semaphore handling. Same explicit-Unlock discipline as the
		// ctx-err block above (v1.4.4 Grok-followup deadlock fix).
		err := sem.Acquire(ctx, 1)
		if err != nil {
			errMutex.Lock()

			errs = append(errs, customerror.New(fmt.Sprintf(`context timeout before mapping "%+v"`, key)))
			errMutex.Unlock()

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
				resBytes, err := json.Marshal(res)
				if err == nil {
					_, writeErr := o.Writer.Write(append(resBytes, '\n'))
					if writeErr != nil {
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
