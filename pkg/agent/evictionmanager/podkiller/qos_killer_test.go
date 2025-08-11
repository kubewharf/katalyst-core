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

package podkiller

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

// TestQoSAwareKiller_Name tests the Name method of qosAwareKiller
func TestQoSAwareKiller_Name(t *testing.T) {
	t.Parallel()

	killer, err := NewQoSAwareKiller(generic.NewQoSConfiguration(), DummyKiller{}, map[string]Killer{
		apiconsts.PodAnnotationQoSLevelSharedCores:    DummyKiller{},
		apiconsts.PodAnnotationQoSLevelDedicatedCores: DummyKiller{},
	})
	if err != nil {
		t.Fatalf("Failed to create QoSAwareKiller: %v", err)
	}

	if killer.Name() != "qos-aware-killer" {
		t.Errorf("Expected name 'qos-aware-killer', got %q", killer.Name())
	}
}

// TestQoSAwareKiller_Evict tests the Evict method of qosAwareKiller with different QoS levels
func TestQoSAwareKiller_Evict(t *testing.T) {
	t.Parallel()

	qosConfig := generic.NewQoSConfiguration()
	qosConfig.QoSClassAnnotationSelector[apiconsts.PodAnnotationQoSLevelDedicatedCores]["custom-qos-level"] = "custom-dedicated-cores"

	killer, err := NewQoSAwareKiller(qosConfig, DummyKiller{}, map[string]Killer{
		apiconsts.PodAnnotationQoSLevelDedicatedCores: DummyKiller{},
	})
	if err != nil {
		t.Fatalf("Failed to create QoSAwareKiller: %v", err)
	}

	tests := []struct {
		name    string
		pod     *v1.Pod
		wantErr bool
	}{
		{
			name: "Evict pod with dedicated cores QoS",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Annotations: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelDedicatedCores,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "Evict pod with unknown QoS to default killer",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
				},
			},
			wantErr: false,
		},
		{
			name: "Evict pod with conflict QoS to default killer",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pod",
					Namespace: "default",
					Annotations: map[string]string{
						apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
						"custom-qos-level":                 "custom-dedicated-cores",
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := killer.Evict(context.Background(), tt.pod, 30, "test-reason", "test-plugin")
			if (err != nil) != tt.wantErr {
				t.Errorf("Evict() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
