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

package oom

import (
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestNativeOOMPriorityCalculator(t *testing.T) {
	t.Parallel()

	qosConfig := generic.NewQoSConfiguration()
	qosConfig.SetExpandQoSLevelSelector(apiconsts.PodAnnotationQoSLevelSharedCores, map[string]string{
		apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
	})
	qosConfig.SetExpandQoSLevelSelector(apiconsts.PodAnnotationQoSLevelDedicatedCores, map[string]string{
		apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
	})
	qosConfig.SetExpandQoSLevelSelector(apiconsts.PodAnnotationQoSLevelReclaimedCores, map[string]string{
		apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
	})

	tests := []struct {
		name              string
		pod               *v1.Pod
		expectAnnotations map[string]string
		want              int
		wantErr           bool
	}{
		{
			name: "pod without both qos level annotation and oom priority annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:        "test-pod-1",
					Annotations: map[string]string{},
				},
			},
			want:    0,
			wantErr: false,
		},
		{
			name: "pod with qos level annotation but without oom priority annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-2",
					Annotations: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			want:    100,
			wantErr: false,
		},
		{
			name: "pod with qos level annotation but with invalid oom priority annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-3",
					Annotations: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelSystemCores,
						apiconsts.PodAnnotationMemoryEnhancementKey: `{"oom_priority": "err"}`,
					},
				},
			},
			want:    0,
			wantErr: true,
		},
		{
			name: "pod with qos level annotation but with oom priority annotation out of range",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-4",
					Annotations: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelReclaimedCores,
						apiconsts.PodAnnotationMemoryEnhancementKey: `{"oom_priority": "41"}`,
					},
				},
			},
			want:    -1,
			wantErr: false,
		},
		{
			name: "pod with qos level annotation and with correct oom priority annotation",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pod-4",
					Annotations: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey:          apiconsts.PodAnnotationQoSLevelReclaimedCores,
						apiconsts.PodAnnotationMemoryEnhancementKey: `{"oom_priority": "-20"}`,
					},
				},
			},
			want:    -20,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nativeOOMPriorityCalculator(qosConfig, tt.pod, 0)
			if (err != nil) != tt.wantErr {
				t.Errorf("nativeOOMPriorityCalculator() error = %v", err)
				return
			}
			if got != tt.want {
				t.Errorf("nativeOOMPriorityCalculator() = %v, want %v", got, tt.want)
			}
		})
	}
}
