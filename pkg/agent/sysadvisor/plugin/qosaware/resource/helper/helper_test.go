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

package helper

import (
	"context"
	"fmt"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
)

func TestPodMatchKind(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		pod       *v1.Pod
		kind      string
		wantMatch bool
	}{
		{
			name: "nil pod",
			pod:  nil,
			kind: "Deployment",
		},
		{
			name: "no owner references",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: nil,
				},
			},
			kind: "Deployment",
		},
		{
			name: "empty owner references",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{},
				},
			},
			kind: "Deployment",
		},
		{
			name: "match kind",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "Deployment"},
					},
				},
			},
			kind:      "Deployment",
			wantMatch: true,
		},
		{
			name: "no match kind",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "Deployment"},
					},
				},
			},
			kind: "StatefulSet",
		},
		{
			name: "multiple owner refs with match",
			pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					OwnerReferences: []metav1.OwnerReference{
						{Kind: "StatefulSet"},
						{Kind: "Deployment"},
					},
				},
			},
			kind:      "Deployment",
			wantMatch: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := PodMatchKind(tt.pod, tt.kind); got != tt.wantMatch {
				t.Errorf("PodMatchKind() = %v, want %v", got, tt.wantMatch)
			}
		})
	}
}

func TestFilterPodsByKind(t *testing.T) {
	t.Parallel()
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod1",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment"},
			},
		},
	}

	pod2 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod2",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "StatefulSet"},
			},
		},
	}

	pod3 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod3",
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Job"},
			},
		},
	}

	tests := []struct {
		name            string
		pods            []*v1.Pod
		skippedPodKinds []string
		wantPods        []string
	}{
		{
			name:            "no pods",
			pods:            nil,
			skippedPodKinds: []string{"Deployment"},
			wantPods:        nil,
		},
		{
			name:            "no skipped kinds",
			pods:            []*v1.Pod{pod1, pod2, pod3},
			skippedPodKinds: nil,
			wantPods:        []string{"pod1", "pod2", "pod3"},
		},
		{
			name:            "skip deployment",
			pods:            []*v1.Pod{pod1, pod2, pod3},
			skippedPodKinds: []string{"Deployment"},
			wantPods:        []string{"pod2", "pod3"},
		},
		{
			name:            "skip multiple kinds",
			pods:            []*v1.Pod{pod1, pod2, pod3},
			skippedPodKinds: []string{"Deployment", "Job"},
			wantPods:        []string{"pod2"},
		},
		{
			name:            "skip all kinds",
			pods:            []*v1.Pod{pod1, pod2, pod3},
			skippedPodKinds: []string{"Deployment", "StatefulSet", "Job"},
			wantPods:        nil,
		},
		{
			name:            "skip unknown kind",
			pods:            []*v1.Pod{pod1, pod2, pod3},
			skippedPodKinds: []string{"DaemonSet"},
			wantPods:        []string{"pod1", "pod2", "pod3"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			filtered := FilterPodsByKind(tt.pods, tt.skippedPodKinds)

			if len(filtered) != len(tt.wantPods) {
				t.Fatalf("FilterPodsByKind() len = %d, want %d", len(filtered), len(tt.wantPods))
			}

			gotNames := make([]string, len(filtered))
			for i, pod := range filtered {
				gotNames[i] = pod.Name
			}

			for i, wantName := range tt.wantPods {
				if gotNames[i] != wantName {
					t.Errorf("FilterPodsByKind()[%d] = %s, want %s", i, gotNames[i], wantName)
				}
			}
		})
	}
}

type fakePodFetcher struct {
	*pod.PodFetcherStub
	err error
}

func (p *fakePodFetcher) GetPod(ctx context.Context, podUID string) (*v1.Pod, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.PodFetcherStub.GetPod(ctx, podUID)
}

type fakeServiceProfilingManager struct {
	*spd.DummyServiceProfilingManager
	err        error
	indicators *v1alpha1.ReclaimResourceIndicators
	baseline   bool
}

func (p *fakeServiceProfilingManager) ServiceExtendedIndicator(_ context.Context, _ metav1.ObjectMeta, indicators interface{}) (bool, error) {
	if p.err != nil {
		return false, p.err
	}

	if indicators != nil {
		indicators.(*v1alpha1.ReclaimResourceIndicators).DisableReclaimLevel = p.indicators.DisableReclaimLevel
	}

	return p.baseline, nil
}

func TestPodDisableReclaimLevel(t *testing.T) {
	t.Parallel()

	// Test cases for PodDisableReclaimLevel function
	tests := []struct {
		name                          string
		podUID                        string
		getPodError                   error
		serviceExtendedIndicatorError error
		baseLine                      bool
		disableReclaimLevel           *v1alpha1.DisableReclaimLevel
		expectedResult                v1alpha1.DisableReclaimLevel
	}{
		{
			name:           "GetPod error",
			podUID:         "pod-uid-1",
			getPodError:    fmt.Errorf("pod not found"),
			expectedResult: v1alpha1.DisableReclaimLevelPod,
		},
		{
			name:                          "ServiceExtendedIndicator error",
			podUID:                        "pod-uid-2",
			serviceExtendedIndicatorError: fmt.Errorf("indicator error"),
			baseLine:                      false,
			disableReclaimLevel:           &[]v1alpha1.DisableReclaimLevel{v1alpha1.DisableReclaimLevelNUMA}[0],
			expectedResult:                v1alpha1.DisableReclaimLevelPod,
		},
		{
			name:                "Pod is baseline",
			podUID:              "pod-uid-3",
			baseLine:            true,
			disableReclaimLevel: &[]v1alpha1.DisableReclaimLevel{v1alpha1.DisableReclaimLevelPod}[0],
			expectedResult:      v1alpha1.DisableReclaimLevelPod,
		},
		{
			name:                "Pod is not baseline with valid level",
			podUID:              "pod-uid-4",
			baseLine:            false,
			disableReclaimLevel: &[]v1alpha1.DisableReclaimLevel{v1alpha1.DisableReclaimLevelNode}[0],
			expectedResult:      v1alpha1.DisableReclaimLevelNode,
		},
		{
			name:                "Pod is not baseline with nil level",
			podUID:              "pod-uid-5",
			baseLine:            false,
			disableReclaimLevel: nil,
			expectedResult:      v1alpha1.DisableReclaimLevelPod,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a mock metaServer
			mockMetaServer := &metaserver.MetaServer{
				MetaAgent: &agent.MetaAgent{},
			}

			// Mock GetPod method
			if tt.getPodError != nil {
				mockMetaServer.MetaAgent.PodFetcher = &fakePodFetcher{
					err: tt.getPodError,
				}
			} else {
				mockMetaServer.MetaAgent.PodFetcher = &fakePodFetcher{
					PodFetcherStub: &pod.PodFetcherStub{
						PodList: []*v1.Pod{
							{
								ObjectMeta: metav1.ObjectMeta{
									UID: types.UID(tt.podUID),
								},
							},
						},
					},
					err: tt.getPodError,
				}
			}

			// Mock ServiceExtendedIndicator method
			if tt.serviceExtendedIndicatorError != nil {
				mockMetaServer.ServiceProfilingManager = &fakeServiceProfilingManager{
					err: tt.serviceExtendedIndicatorError,
				}
			} else {
				indicators := &v1alpha1.ReclaimResourceIndicators{}
				if tt.disableReclaimLevel != nil {
					indicators.DisableReclaimLevel = tt.disableReclaimLevel
				}
				mockMetaServer.ServiceProfilingManager = &fakeServiceProfilingManager{
					err:        tt.serviceExtendedIndicatorError,
					indicators: indicators,
					baseline:   tt.baseLine,
				}
			}

			// Call the function
			result := PodDisableReclaimLevel(context.Background(), mockMetaServer, tt.podUID)

			// Assert the result
			if result != tt.expectedResult {
				t.Errorf("PodDisableReclaimLevel() = %v, want %v", result, tt.expectedResult)
			}
		})
	}
}
