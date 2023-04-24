// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package native

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFilterPodAnnotations(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pod        *v1.Pod
		filterKeys []string
		expected   map[string]string
	}{
		{
			name: "test case 1",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod",
					Annotations: map[string]string{
						"key-1": "val-1",
						"key-2": "val-2",
						"key-3": "val-3",
					},
				},
			},
			filterKeys: []string{
				"key-1",
				"key-2",
				"key-4",
			},
			expected: map[string]string{
				"key-1": "val-1",
				"key-2": "val-2",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("case: %v", tc.name)
			got := FilterPodAnnotations(tc.filterKeys, tc.pod)
			assert.Equal(t, tc.expected, got)
		})
	}
}
