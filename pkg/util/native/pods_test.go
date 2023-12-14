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

package native

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestFilterPodAnnotations(t *testing.T) {
	t.Parallel()

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

func TestGetContainerID(t *testing.T) {
	t.Parallel()

	type args struct {
		pod  *v1.Pod
		name string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "test-1",
			args: args{
				pod: &v1.Pod{
					Status: v1.PodStatus{
						ContainerStatuses: []v1.ContainerStatus{
							{
								Name:        "test-container-1",
								State:       v1.ContainerState{},
								ContainerID: "docker://test-container-id-1",
							},
						},
					},
				},
			},
			want: "test-container-id-1",
			wantErr: func(t assert.TestingT, err error, i ...interface{}) bool {
				return false
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := GetContainerID(tt.args.pod, tt.args.name)
			if !tt.wantErr(t, err, fmt.Sprintf("GetContainerID(%v, %v)", tt.args.pod, tt.args.name)) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetContainerID(%v, %v)", tt.args.pod, tt.args.name)
		})
	}
}

func TestGetContainerEnvs(t *testing.T) {
	t.Parallel()

	type args struct {
		pod           *v1.Pod
		containerName string
		envs          []string
	}
	tests := []struct {
		name string
		args args
		want map[string]string
	}{
		{
			name: "test-1",
			args: args{
				pod: &v1.Pod{
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "test-container-1",
								Env: []v1.EnvVar{
									{
										Name:  "test-env-1",
										Value: "test-value-1",
									},
									{
										Name:  "test-env-2",
										Value: "test-value-2",
									},
									{
										Name:  "test-env-3",
										Value: "test-value-3",
									},
								},
							},
						},
					},
				},
				containerName: "test-container-1",
				envs:          []string{"test-env-1", "test-env-2"},
			},
			want: map[string]string{
				"test-env-1": "test-value-1",
				"test-env-2": "test-value-2",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equalf(t, tt.want, GetContainerEnvs(tt.args.pod, tt.args.containerName, tt.args.envs...), "GetContainerEnvs(%v, %v, %v)", tt.args.pod, tt.args.containerName, tt.args.envs)
		})
	}
}

func TestGetPodHostIPs(t *testing.T) {
	t.Parallel()

	type args struct {
		pod *v1.Pod
	}
	tests := []struct {
		name string
		args args
		want []string
	}{
		{
			name: "test-1",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pod-2",
					},
				},
			},
			want: []string{},
		},
		{
			name: "test-2",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pod-2",
					},
					Status: v1.PodStatus{
						HostIP: "xx:xx:xx:xx",
					},
				},
			},
			want: []string{"xx:xx:xx:xx"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := GetPodHostIPs(tt.args.pod)
			assert.Equalf(t, tt.want, got, "GetPodHostIP(%v)", tt.args.pod)
		})
	}
}

func TestParseHostPortForPod(t *testing.T) {
	t.Parallel()

	type args struct {
		pod *v1.Pod
	}
	tests := []struct {
		name string
		args args
		want int32
	}{
		{
			name: "test-1",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pod-2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Ports: []v1.ContainerPort{
									{
										Name:     "test",
										HostPort: 11,
									},
								},
							},
						},
					},
				},
			},
			want: 11,
		},
		{
			name: "test-1",
			args: args{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pod-2",
					},
				},
			},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := ParseHostPortForPod(tt.args.pod, "test")
			assert.Equalf(t, tt.want, got, "GetPodHostIP(%v)", tt.args.pod)
		})
	}
}
func TestPodIsPending(t *testing.T) {
	type args struct {
		pod *v1.Pod
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "pod is pending",
			args: args{
				pod: &v1.Pod{
					Status: v1.PodStatus{
						Phase: v1.PodPending,
					},
				},
			},
			want: true,
		},
		{
			name: "pod isn't pending",
			args: args{
				pod: &v1.Pod{
					Status: v1.PodStatus{
						Phase: v1.PodRunning,
					},
				},
			},
			want: false,
		},
		{
			name: "nil pod",
			args: args{},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PodIsPending(tt.args.pod); got != tt.want {
				t.Errorf("PodIsPending() = %v, want %v", got, tt.want)
			}
		})
	}
}
