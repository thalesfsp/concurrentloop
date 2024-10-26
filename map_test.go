// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.
//
//nolint:exhaustruct
package concurrentloop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNew_ConcurrentProcessing(t *testing.T) {
	sl1 := []int{1, 2, 3, 4, 5}
	sl2 := []string{"a", "b", "c", "d", "e"}
	sl3 := []float64{1.1, 2.2, 3.3, 4.4, 5.5}
	sl5 := []string{}

	// Create a function that will be called concurrently.
	cF1 := func(_ context.Context, i int) (int, error) {
		return i * 2, nil
	}

	cF2 := func(_ context.Context, s string) (string, error) {
		return s + s, nil
	}

	cF3 := func(_ context.Context, f float64) (float64, error) {
		return f * 2, nil
	}

	cF4 := func(_ context.Context, s string) (string, error) {
		if s == "c" {
			return "", errors.New("error")
		}

		return s, nil
	}

	cF5 := func(_ context.Context, s string) (string, error) {
		return s, nil
	}

	cF6 := func(_ context.Context, s string) (string, error) {
		if s == "c" {
			time.Sleep(2 * time.Second)

			return s, nil
		}

		return s, nil
	}

	// Call the function concurrently.
	r1, err1 := Map(context.Background(), sl1, cF1)
	r2, err2 := Map(context.Background(), sl2, cF2)
	r3, err3 := Map(context.Background(), sl3, cF3)
	r4, err4 := Map(context.Background(), sl2, cF4, WithBatchSize(1))
	r5, err5 := Map(context.Background(), sl5, cF5, WithBatchSize(1))

	// Call the function concurrently.
	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	r6, err6 := Map(ctxWithTimeout, sl2, cF6, WithBatchSize(1))

	if err1 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err1)
		return
	}
	if err2 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err2)
		return
	}
	if err3 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err3)
		return
	}
	if err4 == nil {
		t.Errorf("ConcurrentProcessing() error = %v", err4)
		return
	}
	if err5 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err5)
		return
	}

	// Check the results.
	assert.Equal(t, r1, []int{2, 4, 6, 8, 10})
	assert.Equal(t, len(r1), len(sl1))

	assert.Equal(t, r2, []string{"aa", "bb", "cc", "dd", "ee"})
	assert.Equal(t, len(r2), len(sl2))

	assert.Equal(t, r3, []float64{2.2, 4.4, 6.6, 8.8, 11})
	assert.Equal(t, len(r3), len(sl3))

	assert.Equal(t, r4, []string{"a", "b", "d", "e"})
	assert.Equal(t, 4, len(r4))
	assert.ErrorContains(t, err4, "error")

	assert.Equal(t, r5, []string{})
	assert.Equal(t, 0, len(r5))

	assert.Equal(t, []string{"a", "b"}, r6)
	assert.ErrorContains(t, err6, `context timeout before mapping "d"`)
}

func TestNew_ConcurrentProcessing_WithConcurrency(t *testing.T) {
	sl1 := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	// Create a function that will be called concurrently.
	cF1 := func(_ context.Context, i int) (int, error) {
		return i * 2, nil
	}

	// Call the function concurrently.
	r1, err1 := Map(context.Background(), sl1, cF1, WithBatchSize(1))

	if err1 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err1)
		return
	}

	// Check the results.
	assert.Equal(t, []int{2, 4, 6, 8, 10, 12, 14, 16, 18, 20}, r1)
	assert.Equal(t, len(r1), len(sl1))
}

func TestNew_ConcurrentProcessing_WithLimit(t *testing.T) {
	sl1 := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	// Create a function that will be called concurrently.
	cF1 := func(_ context.Context, i int) (int, error) {
		return i * 2, nil
	}

	// Call the function concurrently.
	r1, err1 := Map(context.Background(), sl1, cF1, WithLimit(3), WithRandomDelayTime(100, 300, time.Millisecond))

	if err1 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err1)
		return
	}

	// Check the results.
	assert.Equal(t, 3, len(r1))
}

func TestMapM(t *testing.T) {
	type TestStruct struct{ A string }

	got, errs := MapM(context.Background(), map[string]TestStruct{
		"1": {A: "a"},
		"2": {A: "b"},
		"3": {A: "c"},
	}, func(_ context.Context, key string, _ TestStruct) (string, error) {
		return key, nil
	})
	if errs != nil {
		t.Fatalf("MapX() error = %v", errs)
	}

	assert.Len(t, got, 3)
}

func TestMapM_withOptions(t *testing.T) {
	type TestStruct struct{ A string }

	got, errs := MapM(context.Background(), map[string]TestStruct{
		"1": {A: "a"},
		"2": {A: "b"},
		"3": {A: "c"},
	}, func(_ context.Context, key string, _ TestStruct) (string, error) {
		return key, nil
	}, WithLimit(2))
	if errs != nil {
		t.Fatalf("MapX() error = %v", errs)
	}

	assert.Len(t, got, 2)
}

//////
// MapCH
//////

func TestNew_ConcurrentProcessingCh(t *testing.T) {
	sl1 := []int{1, 2, 3, 4, 5}
	sl2 := []string{"a", "b", "c", "d", "e"}
	sl3 := []float64{1.1, 2.2, 3.3, 4.4, 5.5}
	sl5 := []string{}

	// Create functions that will be called concurrently with done channel
	cF1 := func(_ context.Context, i int, _ chan<- struct{}) (int, error) {
		return i * 2, nil
	}

	cF2 := func(_ context.Context, s string, _ chan<- struct{}) (string, error) {
		return s + s, nil
	}

	cF3 := func(_ context.Context, f float64, _ chan<- struct{}) (float64, error) {
		return f * 2, nil
	}

	cF4 := func(_ context.Context, s string, _ chan<- struct{}) (string, error) {
		if s == "c" {
			return "", errors.New("error")
		}
		return s, nil
	}

	cF5 := func(_ context.Context, s string, _ chan<- struct{}) (string, error) {
		return s, nil
	}

	cF6 := func(_ context.Context, s string, _ chan<- struct{}) (string, error) {
		if s == "c" {
			time.Sleep(2 * time.Second)
			return s, nil
		}
		return s, nil
	}

	// Test early termination
	cF7 := func(_ context.Context, s string, done chan<- struct{}) (string, error) {
		if s == "c" {
			// Signal termination
			close(done)
			return "", errors.New("terminating early")
		}
		return s, nil
	}

	// Call the function concurrently
	r1, err1 := MapDone(context.Background(), sl1, cF1)
	r2, err2 := MapDone(context.Background(), sl2, cF2)
	r3, err3 := MapDone(context.Background(), sl3, cF3)
	r4, err4 := MapDone(context.Background(), sl2, cF4, WithBatchSize(1))
	r5, err5 := MapDone(context.Background(), sl5, cF5, WithBatchSize(1))

	// Test with context timeout
	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	r6, err6 := MapDone(ctxWithTimeout, sl2, cF6, WithBatchSize(1))

	// Test early termination
	r7, err7 := MapDone(context.Background(), sl2, cF7, WithBatchSize(1))

	// Check for errors
	if err1 != nil {
		t.Errorf("ConcurrentProcessingCh() error = %v", err1)
		return
	}
	if err2 != nil {
		t.Errorf("ConcurrentProcessingCh() error = %v", err2)
		return
	}
	if err3 != nil {
		t.Errorf("ConcurrentProcessingCh() error = %v", err3)
		return
	}
	if err4 == nil {
		t.Errorf("ConcurrentProcessingCh() expected error but got nil")
		return
	}
	if err5 != nil {
		t.Errorf("ConcurrentProcessingCh() error = %v", err5)
		return
	}

	// Check the results
	assert.Equal(t, []int{2, 4, 6, 8, 10}, r1)
	assert.Equal(t, len(sl1), len(r1))

	assert.Equal(t, []string{"aa", "bb", "cc", "dd", "ee"}, r2)
	assert.Equal(t, len(sl2), len(r2))

	assert.Equal(t, []float64{2.2, 4.4, 6.6, 8.8, 11}, r3)
	assert.Equal(t, len(sl3), len(r3))

	assert.Equal(t, []string{"a", "b", "d", "e"}, r4)
	assert.Equal(t, 4, len(r4))
	assert.ErrorContains(t, err4, "error")

	assert.Equal(t, []string{}, r5)
	assert.Equal(t, 0, len(r5))

	assert.Equal(t, []string{"a", "b"}, r6)
	assert.ErrorContains(t, err6, `context timeout before mapping "d"`)

	// Check early termination results
	assert.Equal(t, []string{"a", "b"}, r7)
	assert.ErrorContains(t, err7, "terminating early")
}

func TestNew_ConcurrentProcessing_WithConcurrencyCh(t *testing.T) {
	sl1 := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	// Create a function that will be called concurrently.
	cF1 := func(_ context.Context, i int, _ chan<- struct{}) (int, error) {
		return i * 2, nil
	}

	// Call the function concurrently.
	r1, err1 := MapDone(context.Background(), sl1, cF1, WithBatchSize(1))

	if err1 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err1)
		return
	}

	// Check the results.
	assert.Equal(t, []int{2, 4, 6, 8, 10, 12, 14, 16, 18, 20}, r1)
	assert.Equal(t, len(r1), len(sl1))
}

func TestNew_ConcurrentProcessing_WithLimitCh(t *testing.T) {
	sl1 := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	// Create a function that will be called concurrently.
	cF1 := func(_ context.Context, i int, _ chan<- struct{}) (int, error) {
		return i * 2, nil
	}

	// Call the function concurrently.
	r1, err1 := MapDone(context.Background(), sl1, cF1, WithLimit(3), WithRandomDelayTime(100, 300, time.Millisecond))

	if err1 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err1)
		return
	}

	// Check the results.
	assert.Equal(t, 3, len(r1))
}
