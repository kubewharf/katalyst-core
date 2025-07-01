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
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
