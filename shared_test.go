// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

import (
	"reflect"
	"testing"
)

// Generate a 2D slice of integers.
func TestFlatten2D(t *testing.T) {
	// Create a 2D slice of integers.
	data := [][]int{
		{1, 2, 3},
		{4, 5, 6},
		{7, 8, 9},
	}

	// Flatten the 2D slice.
	result := Flatten2D(data)

	// Create the expected result.
	expected := []int{1, 2, 3, 4, 5, 6, 7, 8, 9}

	// Compare the results.
	if !reflect.DeepEqual(result, expected) {
		t.Errorf("The result %v is not equal to the expected %v", result, expected)
	}
}

func TestSplitSlice(t *testing.T) {
	tests := []struct {
		name      string
		items     []int
		batchSize int
		expected  [][]int
	}{
		{
			name:      "normal case",
			items:     []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			batchSize: 2,
			expected:  [][]int{{1, 2}, {3, 4}, {5, 6}, {7, 8}, {9, 10}},
		},
		{
			name:      "batch size larger than items",
			items:     []int{1, 2, 3},
			batchSize: 5,
			expected:  [][]int{{1, 2, 3}},
		},
		{
			name:      "batch size equals 0",
			items:     []int{1, 2, 3, 4},
			batchSize: 0,
			expected:  nil,
		},
		{
			name:      "empty slice",
			items:     []int{},
			batchSize: 3,
			expected:  [][]int{},
		},
		{
			name:      "batch size equals 1",
			items:     []int{1, 2, 3, 4, 5},
			batchSize: 1,
			expected:  [][]int{{1}, {2}, {3}, {4}, {5}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SplitSlice(tt.items, tt.batchSize)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("got %v, want %v", result, tt.expected)
			}
		})
	}
}
