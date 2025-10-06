/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package acceptance

import (
	"testing"
	"time"
)

var (
	// WaitTimeout is the maximum time to wait for an operation to
	// complete. It is the equivalent to the timeout of e.g.
	// assert.Eventually from testify.
	WaitTimeout = 1 * time.Minute
	// PollInterval is the interval between checks for an operation to
	// complete. It is the equivalent to the polling interval of e.g.
	// assert.Eventually from testify.
	PollInterval = 1 * time.Second
)

// eventually is a modified coal-copy of assert.Eventually from testify
func eventually(t testing.TB, condition func() error, waitFor time.Duration, tick time.Duration, msg string, args ...any) {
	t.Helper()

	ch := make(chan error, 1)
	checkCond := func() { ch <- condition() }

	timer := time.NewTimer(waitFor)
	defer timer.Stop()

	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	var tickC <-chan time.Time

	// Check the condition once first on the initial call.
	go checkCond()

	for {
		select {
		case <-timer.C:
			t.Errorf(msg, args...)
		case <-tickC:
			tickC = nil
			go checkCond()
		case v := <-ch:
			if v == nil {
				return
			}
			t.Logf("Condition not yet satisfied: %v", v)
			tickC = ticker.C
		}
	}
}
