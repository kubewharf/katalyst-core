/*
Copyright 2026 The Katalyst Authors.

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

package pod

import (
	"errors"
	"fmt"
)

var (
	ErrPodNotFound       = errors.New("pod not found")
	ErrContainerNotFound = errors.New("container not found")
)

type podNotFoundError struct {
	podUID string
}

func (e *podNotFoundError) Error() string {
	return fmt.Sprintf("failed to find pod by uid %s", e.podUID)
}

func (e *podNotFoundError) Unwrap() error {
	return ErrPodNotFound
}

// NewPodNotFoundError preserves the historical GetPod error text while
// attaching the package-wide sentinel for errors.Is matching.
func NewPodNotFoundError(podUID string) error {
	return &podNotFoundError{podUID: podUID}
}

func IsPodNotFound(err error) bool {
	return errors.Is(err, ErrPodNotFound)
}
