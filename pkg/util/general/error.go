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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
