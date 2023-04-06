// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestExecute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	fns := []ExecuteFunc[int]{
		func(ctx context.Context) (int, error) {
			time.Sleep(1 * time.Second)
			return 1, nil
		},
		func(ctx context.Context) (int, error) {
			time.Sleep(1 * time.Second)
			return 2, nil
		},
		func(ctx context.Context) (int, error) {
			time.Sleep(1 * time.Second)
			return 3, errors.New("custom error")
		},
	}

	results, errs := Execute(ctx, fns)

	expectedResults1 := []int{1, 2}
	expectedResults2 := []int{2, 1}

	if !assert.ElementsMatch(t, results, expectedResults1) && !assert.ElementsMatch(t, results, expectedResults2) {
		t.Errorf("Execute() results = %v, want %v or %v", results, expectedResults1, expectedResults2)
	}

	assert.Len(t, errs, 1, "Expected errors length to be 1")
	assert.EqualError(t, errs[0], "custom error", "Expected error message to be 'custom error'")
}
