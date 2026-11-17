# Critical defect plan — concurrentloop @ f6837fb9f5f1fdee27c63d6416b8cb94c48a876b

Status: CL-1 is implemented locally on `fix/critical-findings`; pending committed CI and independent review.
The original audit and evidence below are retained. User authorization expanded this task from planning to fixing.

Implemented: Map launch-loop exits reach the locked snapshot block. MapDone snapshots both slices,
accepts send/close on done during launch, slot acquisition and completion, and uses buffered worker
completion notifications instead of spawning cancellation waiters. A consumed done signal is not read twice.
Regressions cover both cancellation exits, late success/error, single-send done, saturated batch, zero values
and no additional waiter. The prior textual-only structural test now parses launch-loop returns as Go AST.
Mechanical lint corrections preserve the configured checks; no new disable or reduced gate was added.
Evidence lives outside the repo: `/Volumes/Dev/Dev/critical-review-20261116/concurrentloop/probes/`.

## CL-1 — Launch-loop early returns hand back shared state while workers still run (data race)

Severity: CRITICAL.
- Kind: correctness / concurrency (data race = undefined behaviour under the Go memory model) and data integrity.
- Reach: the primary public API (`Map`, also `MapDone`), default options, on the ordinary
  "deadline expires while the semaphore is saturated" path (`len(items) > BatchSize`; default BatchSize = NumCPU).
- Effect: a slice the caller already owns is written by a still-running worker after the call
  returned. Multi-word `Result` values (string, slice, interface, struct) can be read torn.
  `MapDone` can also lose or tear entries in the error slice.
- The repo's own suite already hits it: `go test -race -count=5 ./...` reports the race from
  `map_test.go:74` (`r6`). The v1.4.4 test notes call these sites "out of scope ... follow-up"
  (`map_errs_race_test.go:47-49`, `:462-466`). The defect is known and still unfixed.

### Root mechanism (one defect, two public entry points)

A launch loop exits early with `return`, skipping `waitForWaitGroupOrCtx` and the snapshot block.
It then reads or returns `results` / `errs` without the mutex the workers use to write them.

| Site | Lines | What is wrong |
|---|---|---|
| `Map` ctx-err exit | `map.go:192-200` | `return RemoveZeroValues(o.RemoveZeroValues, results), errs`: no `resMutex`, no wait |
| `Map` sem.Acquire-fail exit | `map.go:203-210` | same return, same race (the path seen in probes and in the suite) |
| `Map` worker write | `map.go:343-344` | `results[index] = res` under `resMutex`, after the caller has returned |
| `MapDone` done exit | `map.go:737-741` | returns live `errs` without `errMutex`, no wait |
| `MapDone` ctx exit | `map.go:742-748` | `errs = append(...)` with no `errMutex`, then returns |
| `MapDone` Acquire-fail exit | `map.go:772-778` | `errs = append(...)` with no `errMutex`, then returns |
| `MapDone` worker append | `map.go:793-799` | `errs = append(...)` under `errMutex`, so it conflicts with the unlocked appends |
| `MapDone` final return | `map.go:838-848` | reads `errs` without `errMutex` (safe only because `wg.Wait` completed) |

### Triggering call (reproduced)

```go
ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
defer cancel()
res, _ := concurrentloop.Map(ctx, []int{0, 1, 2},
    func(_ context.Context, v int) (string, error) {
        if v == 0 { time.Sleep(150 * time.Millisecond); return "late-success", nil }
        return "other", nil
    },
    concurrentloop.WithBatchSize(1), concurrentloop.WithRemoveZeroValues(false))
// res[0] == "" right after return; 300ms later res[0] == "late-success"; -race: DATA RACE map.go:344 vs caller
```

### Evidence (raw logs in scratch `probes/`)
- `probe-P1_.log`: shows `CONFIRMED: caller-owned returned slice mutated after Map returned ("" -> "late-success")` and a `WARNING: DATA RACE` between the write at map.go:344 and the caller's read.
- `probe-P1b.log`: with default options, the race is inside `Map`. The read is at map.go:91 (`RemoveZeroValues`, reached from the return at map.go:209) and the write is at map.go:344.
- `probe-P2.log`: `MapDone` has two `DATA RACE` reports, worker append at map.go:794 against the unlocked append at map.go:773. This one depends on timing and did not reproduce in the second run (`probe-P1-finaldeps.log`).
- `probe-P1-finaldeps.log`: P1 still reproduces after the dependency update, so the update neither caused nor fixed it.
- Control `probe-P1Control-finaldeps.log`: same timing with `WithBatchSize(3)`, which leaves through the existing snapshot path. It shows 0 races and no mutation in 3 of 3 runs, so the probe isolates the in-loop sites.
- Suite: `logs/final-test-race-count5.log` has a `DATA RACE` whose stack goes through map.go:209 and map.go:91 from `map_test.go:74`.

### Original bounded fix plan (implemented with the completion-channel refinement above)
1. `Map`: in both in-loop exits, append under `errMutex` with an explicit `Unlock()`, then
   `break indexLoop` instead of `return`. This is the same discipline MapM got in v1.4.4. Control
   then reaches `waitForWaitGroupOrCtx`. Because ctx is done, it returns at once through the ctx
   branch, and the existing block snapshots `results` and `errs` under their locks.
2. `MapDone`: send all three in-loop exits through one labeled break. Do every `errs` append under
   `errMutex`. After the loop, call `waitForWaitGroupOrCtx` (ctx exits) or wait for `wg` (done exit),
   then return snapshots of `results` and `errs` taken under their locks, mirroring Map's block.
   The final return must also read `errs` under `errMutex`.
3. Do not change the public signatures, option semantics or error message text (tests assert
   `context timeout before mapping "d"`).

### Regression and negative-control plan
- Add P1, P1b and the MapDone P2 probe as package tests. Run them with `go test -race -count=20 -run 'P1|P2'`.
  They must be race-free, and `res` must be unchanged 300 ms after return.
- Keep the P1 control (snapshot path) as a passing guard.
- Negative control: revert the `Map` edit. P1 must fail with the mutation message and a race at
  map.go:344 against the caller read. Restore the edit and it must pass. Do the same for MapDone/P2.
- Bypass test: a worker that finishes successfully exactly at the deadline while the caller
  iterates the returned slice, plus a MapDone worker that closes `done` and then appends an error.
  Both must be race-clean under `-race -count=50`.
- Tighten `TestEarlyReturnSnapshotInvariant_StructuralAudit` so every launch-loop exit uses
  `break <label>` (no `return RemoveZeroValues(..., results), errs` inside a launch loop), without
  lowering the existing `count >= 2` assertion.
- Acceptance: `go test -race -count=5 ./...` reports zero DATA RACE lines from map.go:91/209/344/773/794.
  The existing Limit-overshoot flake (`TestMapM_withOptions`) is a separate, non-critical issue.
