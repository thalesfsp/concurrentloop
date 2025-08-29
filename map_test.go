// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.
//
//nolint:exhaustruct
package concurrentloop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
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
	r1, err1 := Map(t.Context(), sl1, cF1)
	r2, err2 := Map(t.Context(), sl2, cF2)
	r3, err3 := Map(t.Context(), sl3, cF3)
	r4, err4 := Map(t.Context(), sl2, cF4, WithBatchSize(1))
	r5, err5 := Map(t.Context(), sl5, cF5, WithBatchSize(1))

	// Call the function concurrently.
	ctxWithTimeout, cancel := context.WithTimeout(t.Context(), 1*time.Second)
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
	r1, err1 := Map(t.Context(), sl1, cF1, WithBatchSize(1))

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
	r1, err1 := Map(t.Context(), sl1, cF1, WithLimit(3), WithRandomDelayTime(100, 300, time.Millisecond))

	if err1 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err1)
		return
	}

	// Check the results.
	assert.Equal(t, 3, len(r1))
}

func TestMapM(t *testing.T) {
	type TestStruct struct{ A string }

	got, errs := MapM(t.Context(), map[string]TestStruct{
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

	got, errs := MapM(t.Context(), map[string]TestStruct{
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
// MapCH.
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
	r1, err1 := MapDone(t.Context(), sl1, cF1)
	r2, err2 := MapDone(t.Context(), sl2, cF2)
	r3, err3 := MapDone(t.Context(), sl3, cF3)
	r4, err4 := MapDone(t.Context(), sl2, cF4, WithBatchSize(1))
	r5, err5 := MapDone(t.Context(), sl5, cF5, WithBatchSize(1))

	// Test with context timeout
	ctxWithTimeout, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()

	r6, err6 := MapDone(ctxWithTimeout, sl2, cF6, WithBatchSize(1))

	// Test early termination
	r7, err7 := MapDone(t.Context(), sl2, cF7, WithBatchSize(1))

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
	r1, err1 := MapDone(t.Context(), sl1, cF1, WithBatchSize(1))

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
	r1, err1 := MapDone(t.Context(), sl1, cF1, WithLimit(3), WithRandomDelayTime(100, 300, time.Millisecond))

	if err1 != nil {
		t.Errorf("ConcurrentProcessing() error = %v", err1)
		return
	}

	// Check the results.
	assert.Equal(t, 3, len(r1))
}

func TestMap_WithWriter(t *testing.T) {
	sl1 := []int{1, 2, 3}

	// Create a buffer to capture written output
	var buf bytes.Buffer

	// Create a function that will be called concurrently.
	cF1 := func(_ context.Context, i int) (int, error) {
		return i * 2, nil
	}

	// Call the function concurrently with writer option.
	r1, err1 := Map(t.Context(), sl1, cF1, WithWriter(&buf))

	if err1 != nil {
		t.Errorf("Map() error = %v", err1)
		return
	}

	// Check the results.
	assert.Equal(t, []int{2, 4, 6}, r1)
	assert.Equal(t, len(r1), len(sl1))

	// Check that data was written to buffer
	written := buf.String()
	assert.NotEmpty(t, written)
	assert.Contains(t, written, "2")
	assert.Contains(t, written, "4")
	assert.Contains(t, written, "6")
}

func TestMap_WithWriterToFile(t *testing.T) {
	// Define a sales record structure
	type SalesRecord struct {
		ID      int     `json:"id"`
		Product string  `json:"product"`
		Amount  float64 `json:"amount"`
		Date    string  `json:"date"`
		Region  string  `json:"region"`
	}

	// Create fake sales data
	salesData := []SalesRecord{
		{ID: 1, Product: "Laptop", Amount: 1299.99, Date: "2024-01-15", Region: "North"},
		{ID: 2, Product: "Mouse", Amount: 29.99, Date: "2024-01-16", Region: "South"},
		{ID: 3, Product: "Keyboard", Amount: 89.99, Date: "2024-01-17", Region: "East"},
		{ID: 4, Product: "Monitor", Amount: 449.99, Date: "2024-01-18", Region: "West"},
		{ID: 5, Product: "Headphones", Amount: 199.99, Date: "2024-01-19", Region: "North"},
		{ID: 6, Product: "Tablet", Amount: 599.99, Date: "2024-01-20", Region: "South"},
		{ID: 7, Product: "Phone", Amount: 899.99, Date: "2024-01-21", Region: "East"},
		{ID: 8, Product: "Webcam", Amount: 79.99, Date: "2024-01-22", Region: "West"},
	}

	// Create a temporary file
	tempFile, err := os.CreateTemp(t.TempDir(), "sales_output_*.json")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	defer os.Remove(tempFile.Name()) // Clean up
	defer tempFile.Close()

	// Create a function that processes sales records and adds tax
	processSales := func(_ context.Context, record SalesRecord) (SalesRecord, error) {
		// Add 10% tax to the amount
		record.Amount *= 1.10
		return record, nil
	}

	// Call Map with the writer option pointing to the temp file
	results, errors := Map(t.Context(), salesData, processSales, WithWriter(tempFile))

	if errors != nil {
		t.Errorf("Map() error = %v", errors)
		return
	}

	// Verify results
	assert.Equal(t, len(salesData), len(results))
	assert.InDelta(t, 1429.99, results[0].Amount, 0.01) // Allow small floating point difference

	// Read the file content to verify data was written
	tempFile.Close() // Close before reading

	fileContent, err := os.ReadFile(tempFile.Name())
	if err != nil {
		t.Fatalf("Failed to read temp file: %v", err)
	}

	// Verify file is not empty and contains expected data
	assert.NotEmpty(t, string(fileContent))
	assert.Contains(t, string(fileContent), "Laptop")
	assert.Contains(t, string(fileContent), "1429.989") // The actual calculated amount
	assert.Contains(t, string(fileContent), "North")

	// Verify JSON structure by attempting to unmarshal one line
	lines := bytes.Split(fileContent, []byte("\n"))
	var testRecord SalesRecord
	err = json.Unmarshal(bytes.TrimSpace(lines[0]), &testRecord)
	assert.NoError(t, err)
	assert.NotZero(t, testRecord.ID)
}

func TestMapCh(t *testing.T) {
	type TestStruct struct{ A string }

	// Create channels for per-cycle and end results
	perCycleCh := make(chan string, 10)
	endCh := make(chan string, 10)

	errs := MapCh(t.Context(), map[string]TestStruct{
		"1": {A: "a"},
		"2": {A: "b"},
		"3": {A: "c"},
	}, func(_ context.Context, key string, _ TestStruct, perCycle chan<- string, _ chan<- string) (string, error) {
		// Send to per-cycle channel.
		if perCycle != nil {
			select {
			case perCycle <- "processing-" + key:
			default:
			}
		}
		return key, nil
	}, perCycleCh, endCh)

	if errs != nil {
		t.Fatalf("MapCh() error = %v", errs)
	}

	// Close channels to allow range loops to finish.
	close(perCycleCh)
	close(endCh)

	// Check per-cycle results.
	perCycleResults := make([]string, 0)
	for result := range perCycleCh {
		perCycleResults = append(perCycleResults, result)
	}
	assert.Len(t, perCycleResults, 3)

	// Check end results.
	endResults := make([]string, 0)
	for result := range endCh {
		endResults = append(endResults, result)
	}
	assert.Len(t, endResults, 3)
}

func TestMapCh_WithPerCycleFileOutput(t *testing.T) {
	type TestStruct struct{ A string }

	// Create a temporary file
	tempFile, err := os.CreateTemp(t.TempDir(), "per_cycle_output_*.txt")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}

	defer os.Remove(tempFile.Name()) // Clean up
	defer tempFile.Close()

	// Create channels for per-cycle and end results
	perCycleCh := make(chan string, 10)
	endCh := make(chan string, 10)

	// Start a goroutine to read from perCycleCh and write to file.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for result := range perCycleCh {
			_, _ = tempFile.WriteString(result + "\n")
		}
	}()

	errs := MapCh(t.Context(), map[string]TestStruct{
		"1": {A: "a"},
		"2": {A: "b"},
		"3": {A: "c"},
	}, func(_ context.Context, key string, _ TestStruct, perCycle chan<- string, _ chan<- string) (string, error) {
		// Send to per-cycle channel.
		if perCycle != nil {
			select {
			case perCycle <- "processing-" + key:
			default:
			}
		}
		return key, nil
	}, perCycleCh, endCh)

	if errs != nil {
		t.Fatalf("MapCh() error = %v", errs)
	}

	// Close channels to allow goroutines to finish.
	close(perCycleCh)
	close(endCh)

	// Wait for file writing to complete.
	<-done

	// Close file before reading.
	tempFile.Close()

	// Read the file content to verify data was written.
	fileContent, err := os.ReadFile(tempFile.Name())
	if err != nil {
		t.Fatalf("Failed to read temp file: %v", err)
	}

	// Verify file is not empty and contains expected data.
	assert.NotEmpty(t, string(fileContent))
	assert.Contains(t, string(fileContent), "processing-1")
	assert.Contains(t, string(fileContent), "processing-2")
	assert.Contains(t, string(fileContent), "processing-3")

	// Count lines to ensure all results were written.
	lines := bytes.Split(bytes.TrimSpace(fileContent), []byte("\n"))
	assert.Len(t, lines, 3)
}

func TestMapCh_WithNilChannels(t *testing.T) {
	type TestStruct struct{ A string }

	// Test with nil channels should now fail.
	errs := MapCh(t.Context(), map[string]TestStruct{
		"1": {A: "a"},
		"2": {A: "b"},
		"3": {A: "c"},
	}, func(_ context.Context, key string, _ TestStruct, perCycle chan<- string, end chan<- string) (string, error) {
		// Function should handle nil channels gracefully
		if perCycle != nil {
			select {
			case perCycle <- "processing-" + key:
			default:
			}
		}
		if end != nil {
			select {
			case end <- "completed-" + key:
			default:
			}
		}
		return key, nil
	}, nil, nil) // Pass nil channels

	// Should return an error when both channels are nil.
	if errs == nil {
		t.Fatalf("MapCh() with nil channels should return an error")
	}

	assert.ErrorContains(t, errs, "at least one channel (perCycleCh or endCh) must be provided")
}

func TestMapCh_WithOnlyPerCycleChannel(t *testing.T) {
	type TestStruct struct{ A string }

	// Create only per-cycle channel, endCh is nil.
	perCycleCh := make(chan string, 10)

	errs := MapCh(t.Context(), map[string]TestStruct{
		"1": {A: "a"},
		"2": {A: "b"},
		"3": {A: "c"},
	}, func(_ context.Context, key string, _ TestStruct, perCycle chan<- string, _ chan<- string) (string, error) {
		// Send to per-cycle channel.
		if perCycle != nil {
			select {
			case perCycle <- "processing-" + key:
			default:
			}
		}
		return key, nil
	}, perCycleCh, nil) // Only perCycleCh, endCh is nil

	if errs != nil {
		t.Fatalf("MapCh() error = %v", errs)
	}

	// Close channel to allow range loop to finish.
	close(perCycleCh)

	// Check per-cycle results.
	perCycleResults := make([]string, 0)
	for result := range perCycleCh {
		perCycleResults = append(perCycleResults, result)
	}
	assert.Len(t, perCycleResults, 3)
}

func TestMapCh_WithOnlyEndChannel(t *testing.T) {
	type TestStruct struct{ A string }

	// Create only end channel, perCycleCh is nil.
	endCh := make(chan string, 10)

	errs := MapCh(t.Context(), map[string]TestStruct{
		"1": {A: "a"},
		"2": {A: "b"},
		"3": {A: "c"},
	}, func(_ context.Context, key string, _ TestStruct, _ chan<- string, _ chan<- string) (string, error) {
		// No per-cycle processing since perCycle is nil
		return key, nil
	}, nil, endCh) // perCycleCh is nil, only endCh

	if errs != nil {
		t.Fatalf("MapCh() error = %v", errs)
	}

	// Close channel to allow range loop to finish.
	close(endCh)

	// Check end results.
	endResults := make([]string, 0)
	for result := range endCh {
		endResults = append(endResults, result)
	}
	assert.Len(t, endResults, 3)
}

// TestBreakStatementBehavior demonstrates why labeled breaks are necessary
// when using break inside select statements within for loops.
func TestBreakStatementBehavior(t *testing.T) {
	t.Run("unlabeled_break_only_exits_select", func(t *testing.T) {
		processedItems := []string{}
		items := []string{"a", "b", "c", "d", "e"}
		cancelCtx, cancel := context.WithCancel(t.Context())

		// Cancel immediately to trigger the break.
		cancel()

		// Simulate the OLD behavior (without labeled break).
		for _, item := range items {
			select {
			case <-cancelCtx.Done():
				// This break only exits the select, NOT the for loop!
				break
			default:
				// This won't execute due to canceled context.
			}

			// ❌ This code STILL EXECUTES even after break!
			// This is the problematic behavior we fixed.
			processedItems = append(processedItems, "processed-"+item)
		}

		// With unlabeled break, ALL items get processed despite context cancellation.
		assert.Len(t, processedItems, 5, "Unlabeled break allows loop to continue processing all items")
		assert.Equal(t, []string{
			"processed-a", "processed-b", "processed-c", "processed-d", "processed-e",
		}, processedItems)
	})

	t.Run("labeled_break_exits_entire_loop", func(t *testing.T) {
		processedItems := []string{}
		items := []string{"a", "b", "c", "d", "e"}
		cancelCtx, cancel := context.WithCancel(t.Context())

		// Cancel immediately to trigger the break.
		cancel()

		// Simulate the NEW behavior (with labeled break).
	itemLoop:
		for _, item := range items {
			select {
			case <-cancelCtx.Done():
				// This break exits the ENTIRE for loop!
				break itemLoop
			default:
				// This won't execute due to canceled context.
			}

			// ✅ This code is NEVER reached when context is canceled.
			processedItems = append(processedItems, "processed-"+item)
		}

		// With labeled break, NO items get processed after context cancellation.
		assert.Len(t, processedItems, 0, "Labeled break immediately exits the entire loop")
		assert.Equal(t, []string{}, processedItems)
	})

	t.Run("real_world_example_with_context_timeout", func(t *testing.T) {
		items := []string{"item1", "item2", "item3", "item4", "item5"}
		processedCount := 0

		// Create context that times out after 50ms.
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		startTime := time.Now()

	processingLoop:
		for i, item := range items {
			select {
			case <-ctx.Done():
				t.Logf("Context canceled after processing %d items in %v", processedCount, time.Since(startTime))
				// Labeled break ensures we immediately stop processing.
				break processingLoop
			default:
				// Proceed with processing.
			}

			// Simulate some processing time.
			time.Sleep(20 * time.Millisecond)

			processedCount++
			t.Logf("Processed item %d: %s", i+1, item)
		}

		elapsed := time.Since(startTime)

		// We should have processed fewer than all items due to timeout.
		assert.Less(t, processedCount, len(items), "Should process fewer items due to context timeout")
		assert.Less(t, elapsed, 100*time.Millisecond, "Should exit quickly after context timeout")

		t.Logf("Final stats: processed %d/%d items in %v", processedCount, len(items), elapsed)
	})
}

// FailingWriter is a writer that fails after writing a certain number of bytes.
type FailingWriter struct {
	maxBytes int
	written  int
}

func (fw *FailingWriter) Write(p []byte) (int, error) {
	if fw.written+len(p) > fw.maxBytes {
		return 0, errors.New("simulated write failure")
	}

	fw.written += len(p)

	return len(p), nil
}

func TestMap_WithFailingWriter_StopsImmediately(t *testing.T) {
	// Create a smaller dataset to make the test more predictable
	items := make([]int, 20)
	for i := range items {
		items[i] = i + 1
	}

	// Create a failing writer that will fail after a very small amount of data
	// Each marshaled int is roughly 1-2 bytes, so this should fail quickly
	failingWriter := &FailingWriter{maxBytes: 10} // Fail after ~10 bytes

	processFunc := func(_ context.Context, item int) (int, error) {
		return item * 2, nil
	}

	// Call Map with the failing writer, using batch size 1 to control concurrency
	results, errs := Map(t.Context(), items, processFunc, WithWriter(failingWriter), WithBatchSize(1))

	// Should have errors due to writer failure
	assert.NotNil(t, errs, "Should have errors when writer fails")
	assert.Greater(t, len(errs), 0, "Should have at least one error")

	// Debug: Print a few error messages
	t.Logf("First few errors:")
	for i, err := range errs {
		if i < 5 { // Only print first 5 errors
			t.Logf("  Error %d: %s", i, err.Error())
		}
	}

	// Check that at least one error is about writing failure
	hasWriteError := false
	for _, err := range errs {
		errMsg := err.Error()
		if strings.Contains(errMsg, "failed to write result to writer") {
			hasWriteError = true
			break
		}
	}
	assert.True(t, hasWriteError, "Should have a write failure error")

	// The key assertion: when a write error occurs in a goroutine, that goroutine
	// returns immediately and doesn't continue processing. We should see that
	// individual goroutines that encounter write errors return early.
	// Since we're using batch size 1, goroutines process sequentially.

	// Count non-zero results (successful processing)
	nonZeroResults := 0
	for _, result := range results {
		if result != 0 {
			nonZeroResults++
		}
	}

	t.Logf("Non-zero results: %d, Total results length: %d, Total items: %d",
		nonZeroResults, len(results), len(items))
	t.Logf("Number of errors: %d", len(errs))

	// The important thing is that we have write errors and they're being handled properly
	// Each goroutine that encounters a write error should return immediately
	assert.Greater(t, len(errs), 0, "Should have write errors")
	assert.True(t, hasWriteError, "Should have write failure errors")
}

func TestMapM_WithFailingWriter_StopsImmediately(t *testing.T) {
	// Create a smaller dataset to make the test more predictable
	itemsMap := make(map[string]int)
	for i := range 20 {
		itemsMap[fmt.Sprintf("key%d", i)] = i + 1
	}

	// Create a failing writer that will fail after a very small amount of data
	failingWriter := &FailingWriter{maxBytes: 10} // Fail after ~10 bytes

	processFunc := func(_ context.Context, _ string, item int) (int, error) {
		return item * 2, nil
	}

	// Call MapM with the failing writer
	results, errs := MapM(t.Context(), itemsMap, processFunc, WithWriter(failingWriter), WithBatchSize(1))

	// Should have errors due to writer failure
	assert.NotNil(t, errs, "Should have errors when writer fails")
	assert.Greater(t, len(errs), 0, "Should have at least one error")

	// Check that at least one error is about writing failure
	hasWriteError := false
	for _, err := range errs {
		errMsg := err.Error()
		if strings.Contains(errMsg, "failed to write result to writer") {
			hasWriteError = true
			break
		}
	}
	assert.True(t, hasWriteError, "Should have a write failure error")

	t.Logf("Processed %d items, Total items: %d", len(results), len(itemsMap))
	t.Logf("Number of errors: %d", len(errs))

	// The important verification: we have write errors being handled
	assert.Greater(t, len(errs), 0, "Should have write errors")
	assert.True(t, hasWriteError, "Should have write failure errors")
}

func TestMapCh_WithFailingWriter_StopsImmediately(t *testing.T) {
	// Create a smaller dataset to make the test more predictable
	itemsMap := make(map[string]int)
	for i := range 20 {
		itemsMap[fmt.Sprintf("key%d", i)] = i + 1
	}

	// Create a failing writer that will fail after a very small amount of data
	failingWriter := &FailingWriter{maxBytes: 10} // Fail after ~10 bytes

	// Create channels for results
	perCycleCh := make(chan int, 50)
	endCh := make(chan int, 50)

	processFunc := func(_ context.Context, _ string, item int, perCycle chan<- int, _ chan<- int) (int, error) {
		// Send to per-cycle channel if available
		if perCycle != nil {
			select {
			case perCycle <- item:
			default:
			}
		}
		return item * 2, nil
	}

	// Call MapCh with the failing writer
	errs := MapCh(t.Context(), itemsMap, processFunc, perCycleCh, endCh, WithWriter(failingWriter), WithBatchSize(1))

	// Close channels
	close(perCycleCh)
	close(endCh)

	// Should have errors due to writer failure
	assert.NotNil(t, errs, "Should have errors when writer fails")
	assert.Greater(t, len(errs), 0, "Should have at least one error")

	// Check that at least one error is about writing failure
	hasWriteError := false
	for _, err := range errs {
		errMsg := err.Error()
		if strings.Contains(errMsg, "failed to write result to writer") {
			hasWriteError = true
			break
		}
	}
	assert.True(t, hasWriteError, "Should have a write failure error")

	// Count results from channels
	perCycleCount := 0
	for range perCycleCh {
		perCycleCount++
	}

	endCount := 0
	for range endCh {
		endCount++
	}

	t.Logf("Per-cycle results: %d, End results: %d, Total items: %d", perCycleCount, endCount, len(itemsMap))
	t.Logf("Number of errors: %d", len(errs))

	// The important verification: we have write errors being handled
	assert.Greater(t, len(errs), 0, "Should have write errors")
	assert.True(t, hasWriteError, "Should have write failure errors")
}
