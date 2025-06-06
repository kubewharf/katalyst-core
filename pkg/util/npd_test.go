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

package util

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

func TestInsertNPDScopedNodeMetrics(t *testing.T) {
	t.Parallel()

	status := &v1alpha1.NodeProfileDescriptorStatus{
		NodeMetrics: []v1alpha1.ScopedNodeMetrics{
			{
				Scope: "test",
				Metrics: []v1alpha1.MetricValue{
					{
						MetricName: "cpu",
						Value:      resource.MustParse("1"),
					},
				},
			},
		},
	}
	metrics := &v1alpha1.ScopedNodeMetrics{
		Scope: "test",
		Metrics: []v1alpha1.MetricValue{
			{
				MetricName: "cpu",
				Value:      resource.MustParse("2"),
			},
		},
	}
	expectedStatus := v1alpha1.NodeProfileDescriptorStatus{
		NodeMetrics: []v1alpha1.ScopedNodeMetrics{
			{
				Scope: "test",
				Metrics: []v1alpha1.MetricValue{
					{
						MetricName: "cpu",
						Value:      resource.MustParse("2"),
					},
				},
			},
		},
	}

	InsertNPDScopedNodeMetrics(status, metrics)
	assert.Equal(t, expectedStatus, *status)

	InsertNPDScopedNodeMetrics(status, nil)
	assert.Equal(t, expectedStatus, *status)
}

func TestInsertNPDScopedPodMetrics(t *testing.T) {
	t.Parallel()

	status := &v1alpha1.NodeProfileDescriptorStatus{
		PodMetrics: []v1alpha1.ScopedPodMetrics{
			{
				Scope: "test",
				PodMetrics: []v1alpha1.PodMetric{
					{
						Name:      "testPod",
						Namespace: "default",
						Metrics: []v1alpha1.MetricValue{
							{
								MetricName: "cpu",
								Value:      resource.MustParse("1"),
							},
						},
					},
				},
			},
		},
	}
	metrics := &v1alpha1.ScopedPodMetrics{
		Scope: "test",
		PodMetrics: []v1alpha1.PodMetric{
			{
				Name:      "testPod",
				Namespace: "default",
				Metrics: []v1alpha1.MetricValue{
					{
						MetricName: "cpu",
						Value:      resource.MustParse("2"),
					},
					{
						MetricName: "memory",
						Value:      resource.MustParse("4Gi"),
					},
				},
			},
		},
	}
	expectedStatus := v1alpha1.NodeProfileDescriptorStatus{
		PodMetrics: []v1alpha1.ScopedPodMetrics{
			{
				Scope: "test",
				PodMetrics: []v1alpha1.PodMetric{
					{
						Name:      "testPod",
						Namespace: "default",
						Metrics: []v1alpha1.MetricValue{
							{
								MetricName: "cpu",
								Value:      resource.MustParse("2"),
							},
							{
								MetricName: "memory",
								Value:      resource.MustParse("4Gi"),
							},
						},
					},
				},
			},
		},
	}

	InsertNPDScopedPodMetrics(status, metrics)
	assert.Equal(t, expectedStatus, *status)

	InsertNPDScopedPodMetrics(status, nil)
	assert.Equal(t, expectedStatus, *status)
}

func Test_Unmarshal(t *testing.T) {
	t.Parallel()
	type args struct {
		npd KCCTargetResourceNPD
	}
	tests := []struct {
		name string
		args args
		want *v1alpha1.NodeProfileDescriptor
	}{
		{
			name: "test unmarshal",
			args: args{
				npd: KCCTargetResourceNPD{
					Unstructured: toTestUnstructured(fakeNPD()),
				},
			},
			want: fakeNPD(),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := &v1alpha1.NodeProfileDescriptor{}
			err := tt.args.npd.Unmarshal(&got)
			assert.NoError(t, err)
			assert.Equalf(t, tt.want, got, "Unmarshal(%v)", tt.args.npd)
		})
	}
}

func Test_DeepCopy(t *testing.T) {
	t.Parallel()
	type args struct {
		npd *KCCTargetResourceNPD
	}
	tests := []struct {
		name string
		args args
		want *KCCTargetResourceNPD
	}{
		{
			name: "deepcopy",
			args: args{
				npd: &KCCTargetResourceNPD{
					Unstructured: toTestUnstructured(fakeNPD()),
				},
			},
			want: &KCCTargetResourceNPD{
				Unstructured: toTestUnstructured(fakeNPD()),
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.args.npd.DeepCopy()
			assert.Equalf(t, tt.want, got, "DeepCopy(%v)", tt.args.npd)
		})
	}
}

func Test_ToUnstructured(t *testing.T) {
	t.Parallel()
	type args struct {
		npd *KCCTargetResourceNPD
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "test to unstructured",
			args: args{
				npd: &KCCTargetResourceNPD{
					Unstructured: toTestUnstructured(fakeNPD()),
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.args.npd.GetUnstructured()
			gotNPD := NewKCCTargetResourceNPD(got)
			assert.Equalf(t, tt.args.npd, gotNPD, "ToUnstructured(%v)", tt.args.npd)
		})
	}
}

func Test_GenerateConfigHash(t *testing.T) {
	t.Parallel()

	type args struct {
		npd KCCTargetResourceNPD
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "empty status",
			args: args{
				npd: KCCTargetResourceNPD{
					Unstructured: toTestUnstructured(func() *v1alpha1.NodeProfileDescriptor {
						return &v1alpha1.NodeProfileDescriptor{
							ObjectMeta: metav1.ObjectMeta{
								Name: "node-1",
							},
						}
					}()),
				},
			},
			want:    "44136fa355b3",
			wantErr: assert.NoError,
		},
		{
			name: "test calculate npd hash",
			args: args{
				npd: KCCTargetResourceNPD{
					Unstructured: toTestUnstructured(fakeNPD()),
				},
			},
			want:    "f8560bfd6669",
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.args.npd.GenerateConfigHash()
			if !tt.wantErr(t, err, fmt.Sprintf("CalculateNPDHash(%v)", tt.args.npd)) {
				return
			}
			assert.Equalf(t, tt.want, got, "CalculateNPDHash(%v)", tt.args.npd)
		})
	}
}

func fakeNPD() *v1alpha1.NodeProfileDescriptor {
	return &v1alpha1.NodeProfileDescriptor{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
		},
		Status: v1alpha1.NodeProfileDescriptorStatus{
			NodeMetrics: []v1alpha1.ScopedNodeMetrics{
				{
					Scope: "resource-level",
					Metrics: []v1alpha1.MetricValue{
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"level": "L1",
							},
							Value: resource.MustParse("4"),
						},
						{
							MetricName: "memory",
							MetricLabels: map[string]string{
								"level": "L1",
							},
							Value: resource.MustParse("4Gi"),
						},
					},
				},
			},
		},
	}
}
