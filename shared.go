// Copyright 2022 The concurrentloop Authors. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package concurrentloop

//////
// Vars, consts, and types.
//////

// ResultCh receives the result from the channel.
type ResultCh[T any] struct {
	Output T
	Error  error
}
