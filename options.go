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
}

//////
// Built-in options.
//////

// WithConcurrency sets the OnFinished function.
func WithConcurrency(concurrency int) Func {
	return func(o Option) Option {
		o.Concurrency = concurrency

		return o
	}
}
