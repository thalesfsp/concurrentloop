// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

//////
// Vars, consts, and types.
//////

// Name of the package.
const Name = "concurrentloop"

// ResultCh receives the result from the channel.
type ResultCh[T any] struct {
	Error  error
	Index  int
	Output T
}

// Flatten2D takes a 2D slice and returns a 1D slice containing all the elements.
func Flatten2D[T any](data [][]T) []T {
	var result []T

	for _, outer := range data {
		result = append(result, outer...)
	}

	return result
}
