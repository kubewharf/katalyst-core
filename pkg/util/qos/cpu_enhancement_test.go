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

package qos

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestGetPodCPUBurstPolicyFromCPUEnhancement(t *testing.T) {
	t.Parallel()

	qosConfig := generic.NewQoSConfiguration()

	tests := []struct {
		name string
		pod  *v1.Pod
		want string
	}{
		{
			name: "dedicated cores pod with dynamic burst policy",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelDedicatedCores,
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"dynamic"}`,
					},
				},
			},
			want: consts.PodAnnotationCPUEnhancementCPUBurstPolicyDynamic,
		},
		{
			name: "shared cores pod with no burst policy returns none burst policy",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
					},
				},
			},
			want: consts.PodAnnotationCPUEnhancementCPUBurstPolicyDefault,
		},
		{
			name: "dedicated cores pod with no burst policy returns none burst policy",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			want: consts.PodAnnotationCPUEnhancementCPUBurstPolicyDefault,
		},
		{
			name: "shared cores pod with static burst policy",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelSharedCores,
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"static"}`,
					},
				},
			},
			want: consts.PodAnnotationCPUEnhancementCPUBurstPolicyStatic,
		},
		{
			name: "shared cores pod with dynamic burst policy",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelSharedCores,
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"dynamic"}`,
					},
				},
			},
			want: consts.PodAnnotationCPUEnhancementCPUBurstPolicyDynamic,
		},
		{
			name: "reclaimed cores pod should have closed burst policy",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.PodAnnotationQoSLevelKey:       consts.PodAnnotationQoSLevelReclaimedCores,
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_policy":"static"}`,
					},
				},
			},
			want: consts.PodAnnotationCPUEnhancementCPUBurstPolicyClosed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := GetPodCPUBurstPolicyFromCPUEnhancement(qosConfig, tt.pod)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetPodCPUBurstPercentFromCPUEnhancement(t *testing.T) {
	t.Parallel()

	qosConfig := generic.NewQoSConfiguration()

	tests := []struct {
		name      string
		pod       *v1.Pod
		want      float64
		wantFound bool
		wantErr   bool
	}{
		{
			name: "pod with no cpu burst percent",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			want:      0,
			wantFound: false,
		},
		{
			name: "pod with cpu burst percent returns parsed value",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_percent":"50"}`,
					},
				},
			},
			want:      50,
			wantFound: true,
		},
		{
			name: "pod with cpu burst percent exceeding 100 returns 100",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_percent":"150"}`,
					},
				},
			},
			want:      100,
			wantFound: true,
		},
		{
			name: "pod with invalid cpu burst percent",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						consts.PodAnnotationCPUEnhancementKey: `{"cpu_burst_percent":"invalid"}`,
					},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, found, err := GetPodCPUBurstPercentFromCPUEnhancement(qosConfig, tt.pod)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetPodCPUBurstPercentFromCPUEnhancement() error = %v, wantErr %v", err, tt.wantErr)
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantFound, found)
		})
	}
}

func TestAnnotationsGetNUMAIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		annotations map[string]string
		numaNodes   []int
		wantResult  []int
		wantErr     bool
	}{
		{
			name:        "empty annotations",
			annotations: map[string]string{},
			numaNodes:   []int{0, 1, 2, 3},
			wantResult:  []int{},
		},
		{
			name: "valid annotations and numa IDs are subset of machine",
			annotations: map[string]string{
				consts.PodAnnotationCPUEnhancementNumaIDs: "0,2,3",
			},
			numaNodes:  []int{0, 1, 2, 3},
			wantResult: []int{0, 2, 3},
		},
		{
			name: "valid annotations in another format and numa IDs are subset of machine",
			annotations: map[string]string{
				consts.PodAnnotationCPUEnhancementNumaIDs: "1-3",
			},
			numaNodes:  []int{0, 1, 2, 3},
			wantResult: []int{1, 2, 3},
		},
		{
			name: "valid annotations but numa IDs are not subset of machine",
			annotations: map[string]string{
				consts.PodAnnotationCPUEnhancementNumaIDs: "0,2,4",
			},
			numaNodes: []int{0, 1, 2, 3},
			wantErr:   true,
		},
		{
			name: "invalid annotations",
			annotations: map[string]string{
				consts.PodAnnotationCPUEnhancementNumaIDs: "invalid",
			},
			numaNodes: []int{0, 1, 2, 3},
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := AnnotationsGetNUMAIDs(tt.annotations, tt.numaNodes, consts.PodAnnotationCPUEnhancementNumaIDs)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				expectedMask, err := machine.NewBitMask(tt.wantResult...)
				assert.NoError(t, err)
				assert.Equal(t, expectedMask, result)
			}
		})
	}
}
