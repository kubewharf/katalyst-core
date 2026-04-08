/*
Copyright 2022 The Katalyst Authors.

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

package general

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

var appendErrorIfSeenTestMu sync.Mutex

func TestErrorSecondSightTracker_appendIfSeen(t *testing.T) {
	t.Parallel()

	type call struct {
		handlerName string
		err         error
	}

	tests := []struct {
		name           string
		nilTracker     bool
		initialErrList []error
		calls          []call
		wantErrStrings []string
	}{
		{
			name:           "same handler, first sight does not append, second sight appends",
			calls:          []call{{handlerName: "h1", err: errors.New("boom")}, {handlerName: "h1", err: errors.New("boom")}},
			wantErrStrings: []string{"boom"},
		},
		{
			name:           "different handlers are isolated",
			calls:          []call{{handlerName: "h1", err: errors.New("boom")}, {handlerName: "h2", err: errors.New("boom")}, {handlerName: "h1", err: errors.New("boom")}},
			wantErrStrings: []string{"boom"},
		},
		{
			name:           "different error strings in same handler do not append on first sight",
			calls:          []call{{handlerName: "h1", err: errors.New("a")}, {handlerName: "h1", err: errors.New("b")}},
			wantErrStrings: nil,
		},
		{
			name:           "nil error is ignored",
			calls:          []call{{handlerName: "h1", err: nil}},
			wantErrStrings: nil,
		},
		{
			name:           "nil receiver does not change errList",
			nilTracker:     true,
			initialErrList: []error{errors.New("keep")},
			calls:          []call{{handlerName: "h1", err: errors.New("boom")}},
			wantErrStrings: []string{"keep"},
		},
		{
			name:           "empty handlerName still works as a key",
			calls:          []call{{handlerName: "", err: errors.New("boom")}, {handlerName: "", err: errors.New("boom")}},
			wantErrStrings: []string{"boom"},
		},
		{
			name:           "second sight append works even when starting with a nil slice",
			calls:          []call{{handlerName: "h1", err: errors.New("boom")}, {handlerName: "h1", err: errors.New("boom")}},
			wantErrStrings: []string{"boom"},
		},
		{
			name:           "existing errList entries are preserved and second sight appends after them",
			initialErrList: []error{errors.New("keep1"), errors.New("keep2")},
			calls:          []call{{handlerName: "h1", err: errors.New("boom")}, {handlerName: "h1", err: errors.New("boom")}},
			wantErrStrings: []string{"keep1", "keep2", "boom"},
		},
		{
			name:           "dynamic error strings do not append on second call if message changes",
			calls:          []call{{handlerName: "h1", err: fmt.Errorf("boom-%d", 1)}, {handlerName: "h1", err: fmt.Errorf("boom-%d", 2)}},
			wantErrStrings: nil,
		},
		{
			name:           "dynamic error strings append only when exact same formatted message repeats",
			calls:          []call{{handlerName: "h1", err: fmt.Errorf("boom-%d", 1)}, {handlerName: "h1", err: fmt.Errorf("boom-%d", 1)}},
			wantErrStrings: []string{"boom-1"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var tracker *ErrorSecondSightTracker
			if tt.nilTracker {
				tracker = nil
			} else {
				tracker = &ErrorSecondSightTracker{}
			}

			errList := tt.initialErrList
			for _, c := range tt.calls {
				errList = tracker.appendIfSeen(errList, c.handlerName, c.err)
			}

			if len(errList) != len(tt.wantErrStrings) {
				t.Fatalf("unexpected errList len: got %d, want %d", len(errList), len(tt.wantErrStrings))
			}
			for i := range tt.wantErrStrings {
				if errList[i] == nil {
					t.Fatalf("errList[%d] is nil, want %q", i, tt.wantErrStrings[i])
				}
				if got := errList[i].Error(); got != tt.wantErrStrings[i] {
					t.Fatalf("errList[%d].Error(): got %q, want %q", i, got, tt.wantErrStrings[i])
				}
			}
		})
	}
}

func TestAppendErrorIfSeen(t *testing.T) {
	t.Parallel()

	// Serialize because this test resets package-level globals.
	appendErrorIfSeenTestMu.Lock()
	t.Cleanup(appendErrorIfSeenTestMu.Unlock)

	resetSecondSightGlobals := func() {
		errorSecondSightTrackerOnce = sync.Once{}
		errorSecondSightTracker = nil
	}

	resetSecondSightGlobals()
	t.Cleanup(resetSecondSightGlobals)

	// First sight does not append; second sight appends.
	var errList []error
	errList = AppendErrorIfSeen(errList, "h1", errors.New("boom"))
	if len(errList) != 0 {
		t.Fatalf("unexpected errList len after first sight: got %d, want 0", len(errList))
	}
	errList = AppendErrorIfSeen(errList, "h1", errors.New("boom"))
	if len(errList) != 1 || errList[0] == nil || errList[0].Error() != "boom" {
		t.Fatalf("unexpected errList after second sight: got %#v", errList)
	}

	// Different handlers are isolated.
	resetSecondSightGlobals()
	errList = nil
	errList = AppendErrorIfSeen(errList, "h1", errors.New("boom"))
	errList = AppendErrorIfSeen(errList, "h2", errors.New("boom"))
	if len(errList) != 0 {
		t.Fatalf("unexpected errList len with isolated handlers: got %d, want 0", len(errList))
	}
	errList = AppendErrorIfSeen(errList, "h1", errors.New("boom"))
	if len(errList) != 1 || errList[0].Error() != "boom" {
		t.Fatalf("unexpected errList after h1 second sight: got %#v", errList)
	}

	// Nil error does not change errList, but still initializes the tracker.
	resetSecondSightGlobals()
	errList = []error{errors.New("keep")}
	errList = AppendErrorIfSeen(errList, "h", nil)
	if len(errList) != 1 || errList[0].Error() != "keep" {
		t.Fatalf("unexpected errList after nil error: got %#v", errList)
	}
	if errorSecondSightTracker == nil {
		t.Fatalf("expected tracker to be initialized")
	}
	errorSecondSightTracker.mu.RLock()
	seenLen := 0
	if errorSecondSightTracker.seen != nil {
		seenLen = len(errorSecondSightTracker.seen)
	}
	errorSecondSightTracker.mu.RUnlock()
	if seenLen != 0 {
		t.Fatalf("expected tracker.seen to be empty after nil error, got %d", seenLen)
	}
}
