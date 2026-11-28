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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPodNotFoundError(t *testing.T) {
	t.Parallel()

	err := NewPodNotFoundError("pod-uid")

	require.EqualError(t, err, "failed to find pod by uid pod-uid")
	require.ErrorIs(t, err, ErrPodNotFound)
	require.True(t, IsPodNotFound(err))
	require.True(t, IsPodNotFound(fmt.Errorf("get pod: %w", err)))
	require.False(t, IsPodNotFound(nil))
	require.False(t, IsPodNotFound(errors.New("failed to find pod by uid pod-uid")))
}
