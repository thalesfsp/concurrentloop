package concurrentloop

import "time"

//////
// Consts, vars and types.
//////

// Func allows to specify message's options.
type Func func(o Option) Option

// Option for the concurrent loop.
type Option struct {
	// BatchSize is the size of the batch.
	BatchSize int

	// The max amount of results to collect before
	Limit int

	// RandomDelayTimeDuration is the unit of the duration (Second, Millisecond, etc.)
	RandomDelayTimeDuration time.Duration

	// RandomDelayTimeMax is the upper limit.
	RandomDelayTimeMax int

	// RandomDelayTimeMin is the lower limit.
	RandomDelayTimeMin int

	// RemoveZeroValues indicates whether to remove zero values from the results.
	RemoveZeroValues bool
}

//////
// Built-in options.
//////

// WithBatchSize sets the size of the batch.
func WithBatchSize(concurrency int) Func {
	return func(o Option) Option {
		o.BatchSize = concurrency

		return o
	}
}

// WithRemoveZeroValues if set to true removes zero values from the results.
func WithRemoveZeroValues(remove bool) Func {
	return func(o Option) Option {
		o.RemoveZeroValues = remove

		return o
	}
}

// WithLimit sets the max amount of results to collect before stopping the loop.
func WithLimit(limit int) Func {
	return func(o Option) Option {
		o.Limit = limit

		return o
	}
}

// WithRandomDelayTime sets the random delay time between each iteration.
func WithRandomDelayTime(min, max int, d time.Duration) Func {
	return func(o Option) Option {
		o.RandomDelayTimeMax = max

		o.RandomDelayTimeMin = min

		o.RandomDelayTimeDuration = d

		return o
	}
}
