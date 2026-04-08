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
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/sets"
)

// ErrorSecondSightTracker tracks errors by their Error() string and only appends
// them into the error list after the same handler produces the same error again.
//
// Because different errors may have the same Error() output, we also split them
// by the name of the handler who called the appendIfSeen function.
type ErrorSecondSightTracker struct {
	// seen is a map of the handler's name to the errors that the handler has stored
	seen map[string]sets.String
	mu   sync.RWMutex
}

// appendIfSeen appends err into errList only if an error with the same
// err.Error() string has been recorded in this store before.
//
// If err is not recorded yet, it will be recorded and NOT appended.
// Returns the updated error list.
func (s *ErrorSecondSightTracker) appendIfSeen(errList []error, handlerName string, err error) []error {
	if s == nil || err == nil {
		return errList
	}
	key := err.Error()

	// Fast path: if handler map + key already exist, only take an RLock.
	s.mu.RLock()
	if s.seen != nil {
		if handlerSeen, ok := s.seen[handlerName]; ok {
			if handlerSeen.Has(key) {
				s.mu.RUnlock()
				return append(errList, err)
			}
		}
	}
	s.mu.RUnlock()

	// Slow path: need to initialize maps/sets or insert a new key.
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.seen == nil {
		s.seen = make(map[string]sets.String)
	}
	if _, ok := s.seen[handlerName]; !ok {
		s.seen[handlerName] = sets.NewString()
	}

	// Re-check after acquiring write lock (another goroutine may have inserted).
	if s.seen[handlerName].Has(key) {
		return append(errList, err)
	}

	s.seen[handlerName].Insert(key)
	return errList
}

var (
	errorSecondSightTrackerOnce sync.Once
	errorSecondSightTracker     *ErrorSecondSightTracker
)

// AppendErrorIfSeen appends the error into the error list if the handler has produced the same error before.
// This is used mainly if there are some transient errors, and we do not want such errors to affect the health of the agent.
func AppendErrorIfSeen(errList []error, handlerName string, err error) []error {
	errorSecondSightTrackerOnce.Do(func() {
		errorSecondSightTracker = &ErrorSecondSightTracker{
			seen: make(map[string]sets.String),
		}
		tracker := errorSecondSightTracker
		// Spawns a goroutine to clear the seen map every 3 minutes
		go func() {
			ticker := time.NewTicker(3 * time.Minute)
			defer ticker.Stop()
			for range ticker.C {
				tracker.mu.Lock()
				tracker.seen = make(map[string]sets.String)
				tracker.mu.Unlock()
			}
		}()
	})
	return errorSecondSightTracker.appendIfSeen(errList, handlerName, err)
}

// common errors
var (
	ErrNotFound    = fmt.Errorf("not found")
	ErrKeyNotExist = errors.New("key does not exist")
)

// IsUnmarshalTypeError check whether is json unmarshal type error
func IsUnmarshalTypeError(err error) bool {
	if _, ok := err.(*json.UnmarshalTypeError); ok {
		return true
	}
	return false
}

func IsErrNotFound(err error) bool {
	return err == ErrNotFound
}

func IsErrKeyNotExist(err error) bool {
	return err == ErrKeyNotExist
}

func IsUnimplementedError(err error) bool {
	// Sources:
	// https://github.com/grpc/grpc/blob/master/doc/statuscodes.md
	// https://github.com/container-storage-interface/spec/blob/master/spec.md
	st, ok := status.FromError(err)
	if !ok {
		// This is not gRPC error. The operation must have failed before gRPC
		// method was called, otherwise we would get gRPC error.
		// We don't know if any previous volume operation is in progress, be on the safe side.
		return false
	}
	switch st.Code() {
	case codes.Unimplemented:
		return true
	}
	return false
}
