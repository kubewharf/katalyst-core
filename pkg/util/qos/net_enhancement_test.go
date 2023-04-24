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

package qos

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestGetPodNetClassID(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pod           *v1.Pod
		expectExist   bool
		expectClassID uint32
	}{
		{
			name: "pod-level net class id exists",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-1",
					Annotations: map[string]string{
						consts.PodAnnotationNetClassKey: "123456",
					},
				},
			},
			expectExist:   true,
			expectClassID: 123456,
		},
		{
			name: "pod-level net class id doesn't exist",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-2",
				},
			},
			expectExist:   false,
			expectClassID: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("case: %v", tc.name)
			gotExist, gotClassID, err := GetPodNetClassID(tc.pod)
			assert.NoError(t, err)
			assert.Equal(t, tc.expectExist, gotExist)
			assert.Equal(t, tc.expectClassID, gotClassID)
		})
	}
}
