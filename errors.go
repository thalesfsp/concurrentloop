// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

import "strings"

//////
// Vars, consts, and types.
//////

// Errors is a slice of errors.
type Errors []error

// Error returns a string representation of the combined errors in the `Errors`
// slice, separated by commas. This method satisfies the `error` interface.
//
//nolint:prealloc
func (e Errors) Error() string {
	var errs []string

	for _, err := range e {
		errs = append(errs, err.Error())
	}

	return strings.Join(errs, ", ")
}
