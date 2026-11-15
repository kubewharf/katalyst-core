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

package resctrl

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPodMonGroupNameRoundTrip(t *testing.T) {
	for _, uid := range []string{"a", "pod-a", "a-b-c"} {
		name := PodMonGroupName(uid)
		require.Equal(t, "pod"+uid, name)
		got, ok := PodUIDFromMonGroupName(name)
		require.True(t, ok)
		require.Equal(t, uid, got)
	}
	for _, name := range []string{"", "pod", "external", "apod-a"} {
		_, ok := PodUIDFromMonGroupName(name)
		require.False(t, ok, name)
	}
}
