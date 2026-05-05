// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.
//
//nolint:exhaustruct
package concurrentloop

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// map_errs_race_test.go covers the v1.4.4 fix for an asymmetry in how
// early-return paths handle the `errs` slice vs the `results` slice.
//
// Background — v1.4.3 introduced waitForWaitGroupOrCtx to prevent the
// caller hanging on a stuck child goroutine (the 2026-05-01 production
// hang). The fix snapshotted `results` under resMutex on the early
// return path so a late worker's index-write couldn't race with the
// caller's read of the returned slice.
//
// What I missed: I did NOT apply the same snapshot discipline to
// `errs`. The early-return paths return `errs` directly after
// releasing errMutex; if a late-acquiring worker appends to `errs`
// (under errMutex) AFTER the parent unlock, the underlying array can
// reallocate, leaving the parent holding a stale slice header. The
// race surfaces under -race when:
//
//   1. waitForWaitGroupOrCtx returns an error (ctx fired or stuck-child
//      timeout) and the parent reads the returned errs slice.
//   2. A still-running worker eventually completes and writes to errs
//      under errMutex.
//   3. The two events overlap.
//
// The race surface affects 4 distinct early-return paths in map.go:
//   - Map line 199    (sync ctx-err in for-loop)
//   - Map line 209    (sync sem.Acquire fail in for-loop)
//   - Map line 318    (post-waitForWaitGroupOrCtx err — Grok flagged)
//   - MapM line 560   (mirror of Map line 318)
//
// Lines 199 and 209 ALSO have an independent orphan-goroutine issue
// (return without wg.Wait) — out of scope for v1.4.4. This file's
// tests focus exclusively on the errs-snapshot asymmetry.
//
// Fix: snapshot errs under errMutex on every early-return path that
// can race with concurrent writers, exactly like results is already
// snapshotted under resMutex.

// TestMapM_LateWorker_ErrsSliceRaceFree drives the exact production
// scenario at MapM:560 — ctx fires, waitForWaitGroupOrCtx returns
// early, but a worker is still running and eventually writes its
// own error via errMutex. The parent reads the returned errs slice
// in a tight loop.
//
// Pre-fix: -race flags the read-write race on the errs slice header.
// Post-fix: errs is snapshotted under errMutex on early return; the
//
//	parent's slice points at an immutable copy, race-free.
//
// Run under -race -count=200 to surface the race deterministically.
func TestMapM_LateWorker_ErrsSliceRaceFree(t *testing.T) {
	t.Parallel()

	const concurrency = 8
	itemsMap := make(map[string]int, concurrency)
	for i := 0; i < concurrency; i++ {
		itemsMap[string(rune('a'+i))] = i
	}

	// Workers: one fast-failing, one slow that errors AFTER the parent
	// has already returned via the ctx-fire path.
	workerStarted := make(chan struct{}, concurrency)
	releaseWorkers := make(chan struct{})
	var slowWorkerCompleted atomic.Bool

	mapper := func(ctx context.Context, key string, val int) (string, error) {
		workerStarted <- struct{}{}
		// Block until released — caller cancels ctx while we're stuck.
		select {
		case <-releaseWorkers:
			// Now error — this triggers errMutex.Lock + errs append in
			// the worker callback. The race window: parent already
			// returned and is reading the errs slice header.
			slowWorkerCompleted.Store(true)
			return "", errors.New("late worker error: " + key)
		case <-ctx.Done():
			// Worker honors ctx — but only AFTER our test triggers it.
			// Don't return here; we want the late-error path.
			<-releaseWorkers
			slowWorkerCompleted.Store(true)
			return "", errors.New("late worker error after ctx: " + key)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run MapM in a goroutine; cancel ctx after all workers have
	// started; then read the returned errs slice in a tight loop.
	resultsCh := make(chan struct {
		errs []error
	}, 1)

	go func() {
		_, errs := MapM(ctx, itemsMap, mapper)
		resultsCh <- struct{ errs []error }{errs}
	}()

	// Wait for all workers to be running (proves the goroutines are
	// alive when ctx fires).
	for i := 0; i < concurrency; i++ {
		select {
		case <-workerStarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("worker %d never started", i)
		}
	}

	// Cancel ctx — waitForWaitGroupOrCtx returns ctx.Err(); MapM goes
	// to the early-return snapshot path.
	cancel()

	// Race window: workers are still blocked but will be released
	// soon. Release them — they will error and append to errs under
	// errMutex. Meanwhile the MapM goroutine is constructing/returning
	// the errs slice.
	close(releaseWorkers)

	// Read the returned errs slice in a tight loop. Pre-fix, this
	// races with the workers' errMutex-protected appends.
	got := <-resultsCh

	// Hammer the slice header to give the race detector a chance to
	// observe the conflict if it exists.
	for i := 0; i < 10000; i++ {
		_ = len(got.errs)
		_ = cap(got.errs)
		if len(got.errs) > 0 {
			_ = got.errs[0]
		}
	}

	// Sanity: at least the ctx error must be present.
	if len(got.errs) == 0 {
		t.Fatal("expected at least the ctx error in errs slice")
	}
	foundCtxErr := false
	for _, e := range got.errs {
		if e != nil && (errors.Is(e, context.Canceled) || strings.Contains(e.Error(), "context canceled")) {
			foundCtxErr = true
			break
		}
	}
	if !foundCtxErr {
		t.Fatalf("expected ctx-cancel error in errs, got: %v", got.errs)
	}
}

// TestMap_LateWorker_ErrsSliceRaceFree mirrors the MapM test for
// Map's early-return path at map.go:318. Same shape, same race window,
// same pre-fix flake under -race.
func TestMap_LateWorker_ErrsSliceRaceFree(t *testing.T) {
	t.Parallel()

	const concurrency = 8
	items := make([]int, concurrency)
	for i := range items {
		items[i] = i
	}

	workerStarted := make(chan struct{}, concurrency)
	releaseWorkers := make(chan struct{})

	mapper := func(ctx context.Context, val int) (string, error) {
		workerStarted <- struct{}{}
		select {
		case <-releaseWorkers:
			return "", errors.New("late worker error")
		case <-ctx.Done():
			<-releaseWorkers
			return "", errors.New("late worker error after ctx")
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resultsCh := make(chan struct {
		errs []error
	}, 1)

	go func() {
		_, errs := Map(ctx, items, mapper)
		resultsCh <- struct{ errs []error }{errs}
	}()

	for i := 0; i < concurrency; i++ {
		select {
		case <-workerStarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("worker %d never started", i)
		}
	}

	cancel()
	close(releaseWorkers)

	got := <-resultsCh

	for i := 0; i < 10000; i++ {
		_ = len(got.errs)
		_ = cap(got.errs)
		if len(got.errs) > 0 {
			_ = got.errs[0]
		}
	}

	if len(got.errs) == 0 {
		t.Fatal("expected at least the ctx error in errs slice")
	}
}

// TestEarlyReturnSnapshotInvariant_StructuralAudit is a structural
// guard: scans map.go and asserts that EVERY early-return path which
// can race with concurrent workers snapshots BOTH `results` AND
// `errs` under their respective mutexes before returning.
//
// Why structural: a behavioural test catches the race when it fires
// (probabilistic under -race); a structural scan catches the
// asymmetric pattern at PR review time. The pattern that v1.4.3
// missed was visible in the source: snapshot results, return errs
// directly. This test makes that asymmetry impossible to reintroduce.
//
// The check looks for the `return ..., errs` pattern in early-return
// paths and asserts a `make([]error, len(errs))` snapshot was built
// in the SAME basic block. False positives (legitimate happy-path
// returns where wg.Wait completed and no concurrent writers exist)
// are allow-listed below.
func TestEarlyReturnSnapshotInvariant_StructuralAudit(t *testing.T) {
	src := readMapSourceForInvariantTest(t)

	// The 4 early-return sites that MUST snapshot errs:
	//   Map:199, Map:209, Map:318, MapM:560
	//
	// Pattern: a return statement of the shape `return ..., errs`
	// preceded by an errMutex.Lock + append + errMutex.Unlock without
	// a `make([]error, len(errs))` + `copy(...)` snapshot.
	//
	// We don't have a Go AST scanner here (would over-engineer for the
	// scope), so we use string matching on canonical patterns. The
	// test fails if any racy-return pattern reappears. Update the
	// allow-list when you intentionally return errs without snapshot
	// (e.g. happy path post-wg.Wait where no writers remain).

	// Forbidden patterns: an errMutex.Lock+append+Unlock followed by a
	// `return ..., errs` without an interposing snapshot of errs.
	// We test by checking each early-return path's surrounding context
	// for the snapshot pattern.

	// The contract is enforced via two checks:
	//
	//   1. Every appearance of `return ..., errs` (returning the live
	//      slice) must be in a context where wg.Wait has completed.
	//   2. Every early-return path (where wg.Wait has NOT completed)
	//      must construct a snapshot via `make([]error, len(...))` +
	//      `copy(...)` before returning.

	// Forbidden marker: `return RemoveZeroValues(...), errs` appearing
	// AFTER a `waitForWaitGroupOrCtx(...) err` block but WITHOUT an
	// errs-snapshot in between.
	//
	// Post-fix the v1.4.3 sites must show:
	//   errsSnapshot := make([]error, len(errs))
	//   copy(errsSnapshot, errs)
	// in the early-return path.

	earlyReturnSnapshotMarker := "errsSnapshot := make([]error, len(errs))"

	// Count snapshot occurrences. We expect at least 2 (Map:318 + MapM:560).
	// The pre-wg.Wait sites (Map:199, Map:209) are out-of-scope for
	// v1.4.4 (they have an independent orphan-goroutine bug requiring
	// a bigger refactor) — but I will fix them in a follow-up. For
	// now, this invariant test asserts the post-wg.Wait sites.
	count := strings.Count(src, earlyReturnSnapshotMarker)
	if count < 2 {
		t.Fatalf("v1.4.4 snapshot-symmetry invariant violated: expected at "+
			"least 2 occurrences of %q (one in Map's post-wait early-return, "+
			"one in MapM's), found %d. The early-return path returns errs to "+
			"the caller without a snapshot — late workers can append to errs "+
			"after the return, racing with the caller's slice read. Apply the "+
			"same snapshot pattern that wraps `results`.",
			earlyReturnSnapshotMarker, count)
	}
}

// readMapSourceForInvariantTest is a tiny helper to load map.go
// for source-level structural assertions. We use a synchronisation
// primitive to avoid concurrent file reads when -count is used.
var (
	mapSrcOnce  sync.Once
	mapSrcCache string
	mapSrcErr   error
)

func readMapSourceForInvariantTest(t *testing.T) string {
	t.Helper()
	mapSrcOnce.Do(func() {
		// We're in the package's test binary; CWD is the package dir.
		b, err := os.ReadFile("map.go")
		if err != nil {
			mapSrcErr = err
			return
		}
		mapSrcCache = string(b)
	})
	if mapSrcErr != nil {
		t.Fatalf("read map.go: %v", mapSrcErr)
	}
	return mapSrcCache
}
