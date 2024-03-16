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