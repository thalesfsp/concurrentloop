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

// SplitSlice splits a slice into batches of a given size.
func SplitSlice[T any](items []T, batchSize int) [][]T {
	if batchSize <= 0 {
		return nil
	}

	if len(items) == 0 {
		return [][]T{}
	}

	batches := make([][]T, 0, (len(items)+batchSize-1)/batchSize)
	for batchSize < len(items) {
		items, batches = items[batchSize:], append(batches, items[0:batchSize:batchSize])
	}

	batches = append(batches, items)

	return batches
}
