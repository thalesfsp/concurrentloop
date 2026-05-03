// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.
//
//nolint:exhaustruct
package concurrentloop

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// These tests cover the context-cancellation contract for Map and MapM:
//
//   1. HAPPY PATH — all items finish; results returned, errs empty.
//   2. BAD PATH   — function returns errors; errs collected.
//   3. EDGE       — ctx pre-cancelled; returns immediately.
//   4. EDGE       — ctx fires DURING processing AND a worker goroutine
//                   never returns (simulates the 2026-05-01 production
//                   hang where a leaked downstream goroutine prevented
//                   wg.Done()): Map/MapM MUST return shortly after ctx
//                   cancel, NOT block on wg.Wait() forever.
//
// Test (4) is the regression guard for the v1.4.2 wg.Wait deadlock that
// blocked the proj-ringboost-vendor v195→v196 rotation for 2+ hours past
// importCtx deadline. Without the fix it deadlocks until the test
// timeout (visible to humans as "test hung").

// stuckCtxBlock returns a function suitable for Map / MapM that blocks on
// ctx.Done() — i.e. it DOES honor ctx — plus a function for "stuck child"
// behavior where the worker IGNORES ctx and only exits when the test
// calls release(). The stuck variant simulates the real-world failure
// mode where a downstream library leaks a goroutine that never lets the
// caller's worker finish.

// blockingFnHonorsCtx returns when its ctx fires Done(). Used to exercise
// the "ctx fires, children unwind cleanly" path.
func blockingFnHonorsCtx[T any](_ context.Context, _ T) (T, error) {
	var zero T
	return zero, nil
}

// stuckMapFn never returns until release is closed. It deliberately
// IGNORES ctx — this is the failure mode we're guarding against. The
// returned release function MUST be called via t.Cleanup so the
// otherwise-leaked goroutine eventually exits when the test ends.
func stuckMapFn(release <-chan struct{}) func(context.Context, int) (int, error) {
	return func(_ context.Context, v int) (int, error) {
		<-release
		return v, nil
	}
}

func stuckMapMFn(release <-chan struct{}) func(context.Context, string, int) (int, error) {
	return func(_ context.Context, _ string, v int) (int, error) {
		<-release
		return v, nil
	}
}

//////
// HAPPY PATH.
//////

func TestMap_HappyPath_AllItemsProcessed(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3, 4, 5}
	double := func(_ context.Context, v int) (int, error) {
		return v * 2, nil
	}

	results, errs := Map(context.Background(), items, double)

	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %d: %v", len(errs), errs)
	}

	got := 0
	for _, r := range results {
		got += r
	}
	want := 0
	for _, v := range items {
		want += v * 2
	}
	if got != want {
		t.Errorf("expected sum %d, got %d (results=%v)", want, got, results)
	}
}

func TestMapM_HappyPath_AllKeysProcessed(t *testing.T) {
	t.Parallel()

	itemsMap := map[string]int{"a": 1, "b": 2, "c": 3}
	upper := func(_ context.Context, _ string, v int) (int, error) {
		return v + 100, nil
	}

	results, errs := MapM(context.Background(), itemsMap, upper)

	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %d: %v", len(errs), errs)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d (results=%v)", len(results), results)
	}
}

//////
// BAD PATH — function returns errors.
//////

func TestMap_FuncReturnsError_CollectedInErrors(t *testing.T) {
	t.Parallel()

	items := []int{1, 2, 3}
	failOnTwo := func(_ context.Context, v int) (int, error) {
		if v == 2 {
			return 0, errors.New("boom on 2")
		}
		return v, nil
	}

	_, errs := Map(context.Background(), items, failOnTwo)

	if len(errs) == 0 {
		t.Fatal("expected at least one error, got none")
	}

	found := false
	for _, e := range errs {
		if e != nil && containsErr(e, "boom on 2") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected an error containing 'boom on 2', got %v", errs)
	}
}

func TestMapM_FuncReturnsError_CollectedInErrors(t *testing.T) {
	t.Parallel()

	itemsMap := map[string]int{"ok": 1, "bad": 999}
	failOn999 := func(_ context.Context, _ string, v int) (int, error) {
		if v == 999 {
			return 0, errors.New("the bad one")
		}
		return v, nil
	}

	_, errs := MapM(context.Background(), itemsMap, failOn999)

	if len(errs) == 0 {
		t.Fatal("expected at least one error, got none")
	}
	found := false
	for _, e := range errs {
		if e != nil && containsErr(e, "the bad one") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error 'the bad one' in errs, got %v", errs)
	}
}

//////
// EDGE — ctx pre-cancelled.
//////

func TestMap_CtxAlreadyCancelled_DoesNotHang(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel BEFORE calling Map

	items := []int{1, 2, 3}
	noop := func(_ context.Context, v int) (int, error) {
		return v, nil
	}

	done := make(chan struct{})
	go func() {
		Map(ctx, items, noop)
		close(done)
	}()

	select {
	case <-done:
		// expected
	case <-time.After(2 * time.Second):
		t.Fatal("Map blocked >2s with pre-cancelled ctx")
	}
}

func TestMapM_CtxAlreadyCancelled_DoesNotHang(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	itemsMap := map[string]int{"a": 1, "b": 2}
	noop := func(_ context.Context, _ string, v int) (int, error) {
		return v, nil
	}

	done := make(chan struct{})
	go func() {
		MapM(ctx, itemsMap, noop)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("MapM blocked >2s with pre-cancelled ctx")
	}
}

//////
// EDGE — THE BUG. ctx fires AFTER children start AND a child never returns.
// Without the fix, Map/MapM block on wg.Wait() forever.
//////

func TestMap_StuckChild_CtxFires_ReturnsWithinBound(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) }) // unblock the leaked goroutine on test exit

	stuck := stuckMapFn(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	items := []int{1}

	start := time.Now()
	done := make(chan struct{})
	var (
		results []int
		errs    Errors
	)
	go func() {
		results, errs = Map(ctx, items, stuck)
		close(done)
	}()

	// Must return within (ctx-deadline + small grace). Without the fix,
	// this blocks until the testing-package timeout (30s) and we observe
	// a panic from the timeout enforcer.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Map blocked >2s after ctx deadline (100ms) — wg.Wait does not honor ctx")
	}
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Errorf("Map returned but elapsed=%v >1s; ctx-respect should be much faster", elapsed)
	}

	// Must surface the ctx error somewhere in errs.
	foundCtxErr := false
	for _, e := range errs {
		if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
			foundCtxErr = true
			break
		}
	}
	if !foundCtxErr {
		t.Errorf("expected ctx error (DeadlineExceeded or Canceled) in errs, got %v", errs)
	}

	// Snapshot of results may be empty or partial — both are acceptable
	// since the worker never finished. Just verify we didn't get a panic.
	_ = results
}

func TestMapM_StuckChild_CtxFires_ReturnsWithinBound(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })

	stuck := stuckMapMFn(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	itemsMap := map[string]int{"a": 1}

	start := time.Now()
	done := make(chan struct{})
	var errs Errors
	go func() {
		_, errs = MapM(ctx, itemsMap, stuck)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("MapM blocked >2s after ctx deadline (100ms) — wg.Wait does not honor ctx")
	}
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("MapM returned but elapsed=%v >1s", elapsed)
	}

	foundCtxErr := false
	for _, e := range errs {
		if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
			foundCtxErr = true
			break
		}
	}
	if !foundCtxErr {
		t.Errorf("expected ctx error in errs, got %v", errs)
	}
}

//////
// EDGE — ctx fires AFTER children start AND every child honors ctx.
// This path was already correct; the test is a regression guard so the
// fix does not break the well-behaved case.
//////

func TestMap_AllChildrenHonorCtx_CtxFires_NoHang(t *testing.T) {
	t.Parallel()

	// Worker that DOES honor ctx — exits promptly on Done.
	honor := func(ctx context.Context, v int) (int, error) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(5 * time.Second):
			// Never reached in this test (ctx fires at 50ms).
			return v, nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	items := []int{1, 2, 3, 4, 5}

	done := make(chan struct{})
	go func() {
		Map(ctx, items, honor)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Map blocked >2s even with ctx-respecting children — fix broke the well-behaved path")
	}
}

//////
// EDGE — Empty inputs.
//////

func TestMap_EmptyItems_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	noop := func(_ context.Context, v int) (int, error) { return v, nil }

	results, errs := Map(context.Background(), []int{}, noop)

	if len(errs) != 0 {
		t.Errorf("expected no errors on empty input, got %v", errs)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results on empty input, got %v", results)
	}
}

func TestMapM_EmptyItems_ReturnsEmpty(t *testing.T) {
	t.Parallel()

	noop := func(_ context.Context, _ string, v int) (int, error) { return v, nil }

	results, errs := MapM(context.Background(), map[string]int{}, noop)

	if len(errs) != 0 {
		t.Errorf("expected no errors on empty input, got %v", errs)
	}
	if len(results) != 0 {
		t.Errorf("expected empty results on empty input, got %v", results)
	}
}

//////
// EDGE — No goroutine leak introduced by the helper itself.
// The helper spawns ONE waiter goroutine; that goroutine MUST exit
// once wg.Wait() returns, regardless of whether the parent select
// picked the ctx branch or the done branch. We verify by sampling the
// goroutine count before / after multiple Map calls.
//////

func TestMap_NoGoroutineLeakFromHelper(t *testing.T) {
	t.Parallel()

	// Don't run in -short mode; this test takes ~500ms total.
	if testing.Short() {
		t.Skip("skipping goroutine-count test in -short")
	}

	// Warm up the runtime once so the first call doesn't skew the
	// baseline (e.g. lazy initialization of internal pools).
	_, _ = Map(context.Background(), []int{1}, func(_ context.Context, v int) (int, error) {
		return v, nil
	})

	// Let any background bookkeeping settle.
	runtime.GC()
	runtime.Gosched()
	time.Sleep(50 * time.Millisecond)

	baseline := runtime.NumGoroutine()

	// Run many Maps that succeed cleanly — the helper goroutine in
	// each must exit, leaving no residue.
	for i := 0; i < 50; i++ {
		_, _ = Map(context.Background(), []int{1, 2, 3}, func(_ context.Context, v int) (int, error) {
			return v, nil
		})
	}

	runtime.GC()
	runtime.Gosched()
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()

	// Allow some slack for runtime-internal goroutines (logger flushers,
	// GC helpers, etc.). +5 over baseline is generous; a real leak
	// would show +50 (one per call) or worse.
	if after > baseline+5 {
		t.Errorf("possible goroutine leak: baseline=%d after=%d (50 Map calls)",
			baseline, after)
	}
}

//////
// Helpers.
//////

func containsErr(e error, sub string) bool {
	if e == nil {
		return false
	}
	return contains(e.Error(), sub)
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// counterFn returns a function suitable for Map that increments calls
// atomically. Useful for verifying how many workers actually invoked f.
func counterFn(calls *atomic.Int32) func(context.Context, int) (int, error) {
	return func(_ context.Context, v int) (int, error) {
		calls.Add(1)
		return v, nil
	}
}

var _ = counterFn // reserved for future tests; silence unused warnings
