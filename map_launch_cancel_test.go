package concurrentloop

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Cancel between the launch loop's checks, rather than relying on scheduling
// to reach the unsafe return. Real cancellation can occur in the same window.
type launchCancelContext struct {
	get      func() context.Context
	cancel   context.CancelFunc
	started  <-chan struct{}
	doneAt   int32
	waitAt   int32
	errAfter bool
	doneN    atomic.Int32
	errN     atomic.Int32
}

func TestMapDone_CancellationAddsNoWaiter(t *testing.T) {
	const calls = 20

	release := make(chan struct{})
	defer close(release)

	before := runtime.NumGoroutine()

	for range calls {
		started, returned := make(chan struct{}), make(chan struct{})

		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			_, _ = MapDone(ctx, []int{1}, func(context.Context, int, chan<- struct{}) (int, error) {
				close(started)
				<-release

				return 1, nil
			})

			close(returned)
		}()

		<-started
		cancel()

		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("MapDone waited for a noncooperative callback")
		}
	}

	// The callbacks themselves remain blocked; the library must not retain
	// another waiter per canceled call. No other tests run in parallel here.
	if after := runtime.NumGoroutine(); after > before+calls+5 {
		t.Fatalf("extra cancellation waiters: before=%d after=%d callbacks=%d", before, after, calls)
	}
}

func TestMapDone_ZeroValueContract(t *testing.T) {
	for _, remove := range []bool{false, true} {
		want := []int{0, 1, 2}
		if remove {
			want = []int{1, 2}
		}

		results, errs := MapDone(t.Context(), []int{0, 1, 2}, func(_ context.Context, v int, _ chan<- struct{}) (int, error) {
			return v, nil
		}, WithRemoveZeroValues(remove))
		if len(errs) != 0 || !reflect.DeepEqual(results, want) {
			t.Fatalf("remove=%v: results=%v errors=%v want=%v", remove, results, errs, want)
		}
	}
}

func newLaunchCancelContext(ctx context.Context, started <-chan struct{}) *launchCancelContext {
	var c launchCancelContext

	c.get = func() context.Context { return ctx }
	c.started = started

	return &c
}

func (c *launchCancelContext) Deadline() (time.Time, bool) {
	return c.get().Deadline()
}

func (c *launchCancelContext) Value(key any) any {
	return c.get().Value(key)
}

func (c *launchCancelContext) Done() <-chan struct{} {
	call := c.doneN.Add(1)
	if c.waitAt != 0 && call == c.waitAt {
		<-c.started
	}

	if c.doneAt != 0 && call == c.doneAt {
		<-c.started
		c.cancel()
	}

	return c.get().Done()
}

func TestMapDone_SingleDoneSend(t *testing.T) {
	for _, waitAt := range []int32{3, 4} {
		t.Run(map[int32]string{3: "launch_select", 4: semaphoreCase}[waitAt], func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})

			var once sync.Once

			t.Cleanup(func() { once.Do(func() { close(release) }) })
			controlled := newLaunchCancelContext(t.Context(), started)
			controlled.waitAt = waitAt
			finished := make(chan struct{}, 1)

			go func() {
				_, _ = MapDone(controlled, []int{1, 2}, func(_ context.Context, _ int, done chan<- struct{}) (int, error) {
					close(started)

					select {
					case done <- struct{}{}:
					case <-t.Context().Done():
						return 0, t.Context().Err()
					}

					<-release

					return 1, nil
				}, WithBatchSize(1))

				finished <- struct{}{}
			}()

			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("MapDone waited for a second done signal or a semaphore slot")
			}
		})
	}
}

func (c *launchCancelContext) Err() error {
	err := c.get().Err()
	if c.cancel != nil && c.doneAt == 0 && c.errN.Add(1) == 2 {
		<-c.started
		c.cancel()

		if c.errAfter {
			return c.get().Err()
		}
	}

	return err
}

const semaphoreCase = "semaphore"

type launchResultWriter chan struct{}

func (w launchResultWriter) Write(p []byte) (int, error) {
	w <- struct{}{}

	return len(p), nil
}

func TestMap_LaunchCancellationSnapshots(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: semaphoreCase, true: "context"}[after], func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})

			var once sync.Once

			t.Cleanup(func() { once.Do(func() { close(release) }) })

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			controlled := newLaunchCancelContext(ctx, started)
			controlled.cancel = cancel
			controlled.errAfter = after
			written := make(launchResultWriter, 1)
			resultCh := make(chan []string, 1)

			go func() {
				res, errs := Map(controlled, []int{1, 2}, func(context.Context, int) (string, error) {
					close(started)
					<-release

					return "late-success", nil
				}, WithBatchSize(1), WithRemoveZeroValues(false), WithWriter(written))
				if len(errs) == 0 {
					t.Error("missing cancellation error")
				}

				resultCh <- res
			}()

			var res []string
			select {
			case res = <-resultCh:
			case <-time.After(time.Second):
				t.Fatal("Map waited for a noncooperative worker after cancellation")
			}

			once.Do(func() { close(release) })
			<-written // The late worker has written results before signaling its writer.

			if !reflect.DeepEqual(res, []string{"", ""}) {
				t.Fatalf("returned snapshot changed after return: %q", res)
			}
		})
	}
}

func TestMapDone_LaunchCancellationSnapshots(t *testing.T) {
	for _, doneAt := range []int32{3, 4} {
		t.Run(map[int32]string{3: "context", 4: semaphoreCase}[doneAt], func(t *testing.T) {
			started, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})

			var once sync.Once

			t.Cleanup(func() { once.Do(func() { close(release) }) })

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			controlled := newLaunchCancelContext(ctx, started)
			controlled.cancel = cancel
			controlled.doneAt = doneAt
			resultCh := make(chan []string, 1)

			go func() {
				res, _ := MapDone(controlled, []int{1, 2}, func(context.Context, int, chan<- struct{}) (string, error) {
					close(started)
					<-release

					defer close(completed)

					return "late-success", nil
				}, WithBatchSize(1), WithRemoveZeroValues(false))
				resultCh <- res
			}()

			var res []string
			select {
			case res = <-resultCh:
			case <-time.After(time.Second):
				t.Fatal("MapDone waited for a noncooperative worker after cancellation")
			}

			once.Do(func() { close(release) })
			<-completed
			// Reading continuously also exercises the caller against a late write
			// under -race; completed only joins the callback, not the library write.
			for range 10000 {
				if !reflect.DeepEqual(res, []string{"", ""}) {
					t.Fatalf("returned snapshot changed after return: %q", res)
				}
			}
		})
	}
}

func TestMapDone_DoneSignalSnapshotsLateError(t *testing.T) {
	closed, release := make(chan struct{}), make(chan struct{})

	var once sync.Once

	t.Cleanup(func() { once.Do(func() { close(release) }) })

	finished := make(chan Errors, 1)

	go func() {
		_, errs := MapDone(t.Context(), []int{1}, func(_ context.Context, _ int, done chan<- struct{}) (int, error) {
			close(done)
			close(closed)
			<-release

			return 0, errors.New("late error")
		})
		finished <- errs
	}()

	<-closed

	select {
	case errs := <-finished:
		once.Do(func() { close(release) })

		for range 10000 {
			if len(errs) != 0 {
				t.Fatal("late error changed the returned snapshot")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("MapDone ignored done while a callback remained blocked")
	}
}

func TestMapDone_LaunchCancellationCollectsErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	started := make(chan struct{})

	var once sync.Once

	controlled := newLaunchCancelContext(ctx, started)
	controlled.cancel = cancel
	controlled.doneAt = 9

	items := []int{1, 2, 3, 4, 5}

	_, errs := MapDone(controlled, items, func(ctx context.Context, _ int, _ chan<- struct{}) (int, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()

		return 0, errors.New("worker cancellation")
	}, WithBatchSize(len(items)))
	if len(errs) == 0 {
		t.Fatal("missing cancellation error")
	}

	for range 10000 {
		_ = errs.Error()
	}
}
