package concurrentloop

//////
// Consts, vars and types.
//////

// Func allows to specify message's options.
type Func func(o Option) Option

// Option for the concurrent loop.
type Option struct {
	// Concurrency is the number of concurrent goroutines that will be used.
	Concurrency int

	// RemoveZeroValues indicates whether to remove zero values from the results.
	RemoveZeroValues bool
}

//////
// Built-in options.
//////

// WithConcurrency sets the concurrency.
func WithConcurrency(concurrency int) Func {
	return func(o Option) Option {
		o.Concurrency = concurrency

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
