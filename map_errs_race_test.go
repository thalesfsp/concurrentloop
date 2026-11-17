// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.
//
//nolint:exhaustruct

package concurrentloop

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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
// Launch-loop exits are now covered by map_launch_cancel_test.go as well as
// the structural guard below; they must reach the common snapshot path.
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
	for i := range concurrency {
		itemsMap[string(rune('a'+i))] = i
	}

	// Workers: one fast-failing, one slow that errors AFTER the parent
	// has already returned via the ctx-fire path.
	workerStarted := make(chan struct{}, concurrency)
	releaseWorkers := make(chan struct{})

	var slowWorkerCompleted atomic.Bool

	mapper := func(ctx context.Context, key string, _ int) (string, error) {
		workerStarted <- struct{}{}
		// Block until released — caller cancels ctx while we're stuck.
		select {
		case <-releaseWorkers:
			// Now error — this triggers errMutex.Lock + errs append in
			// the worker callback. The race window: parent already
			// returned and is reading the errs slice header.
			slowWorkerCompleted.Store(true)
			return key, errors.New("late worker error: " + key)
		case <-ctx.Done():
			// Worker honors ctx — but only AFTER our test triggers it.
			// Don't return here; we want the late-error path.
			<-releaseWorkers
			slowWorkerCompleted.Store(true)

			return key, errors.New("late worker error after ctx: " + key)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Run MapM in a goroutine; cancel ctx after all workers have
	// started; then read the returned errs slice in a tight loop.
	resultsCh := make(chan struct {
		errs []error
	}, 1)

	go func() {
		// WithBatchSize(concurrency) guarantees all `concurrency` workers
		// run at once regardless of runtime.NumCPU(); the default BatchSize
		// (= NumCPU) lets only NumCPU workers start on low-core CI runners,
		// so the "wait for all workers to start" loop below would time out.
		_, errs := MapM(ctx, itemsMap, mapper, WithBatchSize(concurrency))
		resultsCh <- struct{ errs []error }{errs}
	}()

	// Wait for all workers to be running (proves the goroutines are
	// alive when ctx fires).
	for i := range concurrency {
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
	for range 10000 {
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
			return fmt.Sprintf("item-%d", val), errors.New("late worker error")
		case <-ctx.Done():
			<-releaseWorkers
			return fmt.Sprintf("item-%d", val), errors.New("late worker error after ctx")
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	resultsCh := make(chan struct {
		errs []error
	}, 1)

	go func() {
		// WithBatchSize(concurrency): see the sibling MapM race test — the
		// default BatchSize (= NumCPU) would start only NumCPU workers on
		// low-core CI runners, timing out the "all workers started" wait.
		_, errs := Map(ctx, items, mapper, WithBatchSize(concurrency))
		resultsCh <- struct{ errs []error }{errs}
	}()

	for i := range concurrency {
		select {
		case <-workerStarted:
		case <-time.After(2 * time.Second):
			t.Fatalf("worker %d never started", i)
		}
	}

	cancel()
	close(releaseWorkers)

	got := <-resultsCh

	for range 10000 {
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

// TestMapM_KeyLoopCtxErr_DoesNotSelfDeadlock guards against the
// v1.4.4 Grok-followup deadlock: when ctx fires AFTER the keyLoop's
// top `select <-ctx.Done()` but BEFORE the second `if ctx.Err() != nil`
// check, the second check pre-fix did:
//
//	errMutex.Lock()
//	defer errMutex.Unlock()    // ← BUG: defer fires on MapM return,
//	errs = append(errs, ...)   //         not on `break keyLoop`
//	break keyLoop
//
// After the break, execution reaches the post-wait snapshot block at
// the bottom of MapM (the v1.4.4 fix site), which calls
// errMutex.Lock() AGAIN on the same goroutine. Go's sync.Mutex is
// non-recursive — the second Lock blocks forever, deadlocking MapM.
//
// Pre-fix: this test hangs (deadlock) and times out via the t.Run
// deadline. Post-fix (explicit Unlock before break): returns within
// a few milliseconds.
//
// Run under `-race -timeout 30s` to ensure a hang surfaces as a
// deterministic test failure, not an indefinite stall.
func TestMapM_KeyLoopCtxErr_DoesNotSelfDeadlock(t *testing.T) {
	t.Parallel()

	// We need ctx to fire WHILE the keyLoop is iterating but past the
	// top select-default. The `RandomDelayTime*` options inject a
	// sleep AFTER the top-select but BEFORE the second `if ctx.Err()`
	// check — which gives us a deterministic window in which to
	// cancel ctx and force the keyLoop into the racy second-check
	// branch.
	itemsMap := make(map[string]int, 4)
	for i := range 4 {
		itemsMap[string(rune('a'+i))] = i
	}

	mapper := func(_ context.Context, _ string, _ int) (string, error) {
		// Workers themselves are fast; the keyLoop body is what we
		// want to slow down so ctx can fire mid-iteration.
		return "", nil
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	// Bound the entire test on 5s — pre-fix deadlock manifests as an
	// indefinite hang; we want a clean t.Fatalf under timeout.
	done := make(chan struct{})

	go func() {
		// Inject a 50ms randomness sleep per iteration. This widens
		// the window between top-select-default and the second
		// ctx.Err check enough to make ctx-cancel deterministic.
		_, _ = MapM(ctx, itemsMap, mapper,
			WithRandomDelayTime(1, 2, 50*time.Millisecond),
		)

		close(done)
	}()

	// Cancel ctx after a short delay so it fires while a keyLoop
	// iteration is in its randomness sleep.
	time.Sleep(25 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// MapM returned within the deadline — no deadlock.
	case <-time.After(5 * time.Second):
		t.Fatal("MapM did not return within 5s after ctx cancel — " +
			"likely the v1.4.4 keyLoop ctx-err defer-Unlock deadlock " +
			"reintroduced. Check `if ctx.Err() != nil` and " +
			"`if err := sem.Acquire(...)` blocks in MapM keyLoop: " +
			"they MUST use explicit errMutex.Unlock() before " +
			"`break keyLoop`, NOT defer (defer fires on function " +
			"return, leaving the mutex held when the post-wait " +
			"snapshot block tries to re-Lock).")
	}
}

// TestKeyLoopMutexDiscipline_StructuralAudit asserts the absence of
// the deadlock pattern in MapM's keyLoop: `errMutex.Lock()` followed
// by `defer errMutex.Unlock()` followed by `break keyLoop`. Same
// pattern as TestEarlyReturnSnapshotInvariant_StructuralAudit:
// catches the regression at PR review time without needing the timing
// window to fire.
func TestKeyLoopMutexDiscipline_StructuralAudit(t *testing.T) {
	src := readMapSourceForInvariantTest(t)

	// The DEADLOCK pattern is `defer errMutex.Unlock()` followed
	// within ~6 lines by `break <label>`. defer fires on function
	// return, NOT on labeled-loop break — so the mutex stays held
	// when execution continues past the loop and the post-wait
	// snapshot block tries to errMutex.Lock again on the same
	// goroutine, self-deadlocking.
	//
	// SAFE patterns (NOT flagged):
	//   - `defer errMutex.Unlock()` followed by `return` (defer fires
	//     correctly on the return).
	//   - `defer errMutex.Unlock()` inside a worker goroutine that
	//     ends via `return` (defer fires when the worker goroutine's
	//     func returns — no follow-on Lock by the same goroutine).
	//
	// We scan line-by-line: for each `defer errMutex.Unlock()`, peek
	// the next 6 lines; if a `break <label>` appears before any
	// `return` or end-of-block, the site is dangerous.
	lines := strings.Split(src, "\n")

	var offending []string

	for i, line := range lines {
		if !strings.Contains(line, "defer errMutex.Unlock()") {
			continue
		}

		end := min(i+7, len(lines))

		for j := i + 1; j < end; j++ {
			next := lines[j]
			// `return` first → safe pattern; stop scanning this site.
			if strings.Contains(next, "return ") || strings.Contains(next, "return\t") {
				break
			}
			// `break <something>` (with a label) → DEADLOCK pattern.
			// Bare `break` inside a `select` is fine because select
			// is itself terminating — but our convention here is that
			// labeled breaks are the dangerous shape. Match `break <ident>`.
			trimmed := strings.TrimSpace(next)
			if strings.HasPrefix(trimmed, "break ") && trimmed != "break" {
				start := max(i-2, 0)

				ctxEnd := min(j+1, len(lines))

				offending = append(offending,
					"map.go:"+itoa(i+1)+" (defer) → map.go:"+itoa(j+1)+" (break):\n"+
						strings.Join(lines[start:ctxEnd], "\n"))

				break
			}
		}
	}

	if len(offending) > 0 {
		t.Fatalf("v1.4.4 deadlock-pattern reintroduced: %d site(s) of "+
			"`defer errMutex.Unlock()` followed by `break <label>` found in "+
			"map.go.\n\ndefer fires on FUNCTION return, NOT on labeled-loop "+
			"break — leaving errMutex held when execution continues past the "+
			"loop and the post-wait snapshot block (or wg.Wait + worker err "+
			"path) tries to re-Lock errMutex, self-deadlocking the goroutine.\n\n"+
			"Use explicit errMutex.Unlock() BEFORE `break <label>`. Safe "+
			"patterns (defer + return, defer in worker goroutine ending in "+
			"return) are NOT flagged.\n\nOffending sites:\n%s",
			len(offending), strings.Join(offending, "\n\n"))
	}
}

// itoa is a tiny strconv-free integer-to-string helper (avoid pulling
// strconv just for failure messages).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var buf [20]byte

	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}

	return string(buf[i:])
}

// TestEarlyReturnSnapshotInvariant_StructuralAudit keeps the existing snapshot
// guard and checks launch-loop returns structurally. Counting snapshot markers
// alone missed the unsafe exits before the wait/snapshot block.
func TestEarlyReturnSnapshotInvariant_StructuralAudit(t *testing.T) {
	src := readMapSourceForInvariantTest(t)
	if strings.Count(src, "errsSnapshot := make([]error, len(errs))") < 2 {
		t.Fatal("Map and MapM must retain their locked error snapshots")
	}

	file, err := parser.ParseFile(token.NewFileSet(), "map.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || (fn.Name.Name != "Map" && fn.Name.Name != "MapDone") {
			continue
		}

		ast.Inspect(fn.Body, func(node ast.Node) bool {
			loop, ok := node.(*ast.RangeStmt)
			if !ok {
				return true
			}

			ast.Inspect(loop.Body, func(node ast.Node) bool {
				ret, ok := node.(*ast.ReturnStmt)
				if !ok {
					return true
				}

				for _, result := range ret.Results {
					ast.Inspect(result, func(node ast.Node) bool {
						ident, ok := node.(*ast.Ident)
						if ok && (ident.Name == "results" || ident.Name == "errs") {
							t.Errorf("%s returns live %s from its launch loop", fn.Name.Name, ident.Name)
						}

						return true
					})
				}

				return true
			})

			return false
		})
	}
}

// readMapSourceForInvariantTest is a tiny helper to load map.go
// for source-level structural assertions. We use a synchronisation
// primitive to avoid concurrent file reads when -count is used.
var (
	mapSrcOnce  sync.Once
	mapSrcCache string
	errMapSrc   error
)

func readMapSourceForInvariantTest(t *testing.T) string {
	t.Helper()
	mapSrcOnce.Do(func() {
		// We're in the package's test binary; CWD is the package dir.
		b, err := os.ReadFile("map.go")
		if err != nil {
			errMapSrc = err
			return
		}

		mapSrcCache = string(b)
	})

	if errMapSrc != nil {
		t.Fatalf("read map.go: %v", errMapSrc)
	}

	return mapSrcCache
}
