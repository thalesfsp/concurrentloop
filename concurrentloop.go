// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

// resultCh receives the result from the channel.
type resultCh[T any] struct {
	Output T
	Error  error
}

// Func is the function to be called concurrently.
type Func[T any, Result any] func(T) (Result, error)

// Run calls the `Func` concurrently, and returns its
// results.
func Run[T any, Result any](sl []T, f Func[T, Result]) ([]Result, []error) {
	// Create a channel to receive the results.
	resultsCh := make(chan resultCh[Result])
	defer close(resultsCh)

	// Concurrently call, and send the result to the channel.
	for _, t := range sl {
		t := t

		go func(sl []T) {
			result, err := f(t)
			resultsCh <- resultCh[Result]{Output: result, Error: err}
		}(sl)
	}

	var (
		results []Result
		errs    []error
	)

	for range sl {
		result := <-resultsCh

		if result.Error != nil {
			errs = append(errs, result.Error)
		} else {
			results = append(results, result.Output)
		}
	}

	return results, errs
}
