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

package reactor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

type fakePodAllocation struct {
	commonstate.AllocationMeta
	annotations map[string]string
}

func (f fakePodAllocation) NeedUpdateAllocation(pod *v1.Pod) bool {
	if pod.Annotations == nil {
		return true
	}
	for k, v := range f.annotations {
		if pod.Annotations[k] != v {
			return true
		}
	}
	return false
}

func (f fakePodAllocation) UpdateAllocation(pod *v1.Pod) error {
	if f.annotations == nil {
		return nil
	}
	for k, v := range f.annotations {
		if pod.Annotations == nil {
			pod.Annotations = make(map[string]string)
		}
		pod.Annotations[k] = v
	}
	return nil
}

func Test_podAllocationReactor_UpdateAllocation(t *testing.T) {
	t.Parallel()

	type fields struct {
		podFetcher pod.PodFetcher
		client     kubernetes.Interface
	}
	type args struct {
		allocation *fakePodAllocation
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantPod *v1.Pod
		wantErr bool
	}{
		{
			name: "update_annotation",
			fields: fields{
				podFetcher: &pod.PodFetcherStub{
					PodList: []*v1.Pod{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "test-1",
								Namespace: "test",
								UID:       "test-1-uid",
							},
						},
					},
				},
				client: fake.NewSimpleClientset(
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-1",
							Namespace: "test",
							UID:       "test-1-uid",
						},
					},
				),
			},
			args: args{
				allocation: &fakePodAllocation{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "test-1-uid",
						PodNamespace:   "test",
						PodName:        "test-1",
						ContainerName:  "container-1",
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
					},
					annotations: map[string]string{
						"aa": "bb",
					},
				},
			},
			wantPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-1",
					Namespace: "test",
					UID:       types.UID("test-1-uid"),
					Annotations: map[string]string{
						"aa": "bb",
					},
				},
			},
		},
		{
			name: "overwrite_annotation",
			fields: fields{
				podFetcher: &pod.PodFetcherStub{
					PodList: []*v1.Pod{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "test-1",
								Namespace: "test",
								UID:       "test-1-uid",
								Annotations: map[string]string{
									"aa": "cc",
								},
							},
						},
					},
				},
				client: fake.NewSimpleClientset(
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-1",
							Namespace: "test",
							UID:       "test-1-uid",
						},
					},
				),
			},
			args: args{
				allocation: &fakePodAllocation{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "test-1-uid",
						PodNamespace:   "test",
						PodName:        "test-1",
						ContainerName:  "container-1",
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
					},
					annotations: map[string]string{
						"aa": "bb",
					},
				},
			},
			wantPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-1",
					Namespace: "test",
					UID:       types.UID("test-1-uid"),
					Annotations: map[string]string{
						"aa": "bb",
					},
				},
			},
		},
		{
			name: "pod_fallback_to_api-server",
			fields: fields{
				podFetcher: &pod.PodFetcherStub{
					PodList: []*v1.Pod{},
				},
				client: fake.NewSimpleClientset(
					&v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Name:      "test-1",
							Namespace: "test",
							UID:       "test-1-uid",
							Annotations: map[string]string{
								"aa": "bb",
							},
						},
					},
				),
			},
			args: args{
				allocation: &fakePodAllocation{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:         "test-1-uid",
						PodNamespace:   "test",
						PodName:        "test-1",
						ContainerName:  "container-1",
						ContainerType:  pluginapi.ContainerType_MAIN.String(),
						ContainerIndex: 0,
					},
					annotations: map[string]string{
						"aa": "bb",
					},
				},
			},
			wantPod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-1",
					Namespace: "test",
					UID:       types.UID("test-1-uid"),
					Annotations: map[string]string{
						"aa": "bb",
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := NewPodAllocationReactor(tt.fields.podFetcher, tt.fields.client)
			if err := r.UpdateAllocation(context.Background(), tt.args.allocation); (err != nil) != tt.wantErr {
				t.Errorf("UpdateAllocation() error = %v, wantErr %v", err, tt.wantErr)
			}

			getPod, err := tt.fields.client.CoreV1().Pods(tt.args.allocation.PodNamespace).Get(context.Background(), tt.args.allocation.PodName, metav1.GetOptions{})
			if err != nil {
				t.Errorf("GetPod() error = %v, wantErr %v", err, tt.wantErr)
			} else {
				assert.Equal(t, tt.wantPod, getPod)
			}
		})
	}
}
