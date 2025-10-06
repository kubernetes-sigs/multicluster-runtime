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
	"context"
	"errors"
	"testing"
)

// ErrorHandler is a function that handles errors from goroutines
// started by consumers of the accetpance tests.
// The ErrorHandler will automatically ignore nil values and context.Canceled.
type ErrorHandler func(error)

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func errorHandler(t testing.TB, msg string) ErrorHandler {
	t.Helper()
	return func(err error) {
		t.Helper()
		if ignoreCanceled(err) == nil {
			return
		}
		t.Errorf("%s: %v", msg, err)
	}
}
