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
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestPodUniqKeyCmpFunc(t *testing.T) {
	t.Parallel()

	type args struct {
		i1 *v1.Pod
		i2 *v1.Pod
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "greater",
			args: args{
				i1: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-a",
						Namespace: "namespace-1",
					},
				},
				i2: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-b",
						Namespace: "namespace-1",
					},
				},
			},
			want: 1,
		},
		{
			name: "equal",
			args: args{
				i1: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-a",
						Namespace: "namespace-1",
					},
				},
				i2: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-a",
						Namespace: "namespace-1",
					},
				},
			},
			want: 0,
		},
		{
			name: "smaller",
			args: args{
				i1: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-b",
						Namespace: "namespace-1",
					},
				},
				i2: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod-a",
						Namespace: "namespace-1",
					},
				},
			},
			want: -1,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equalf(t, tt.want, PodUniqKeyCmpFunc(tt.args.i1, tt.args.i2), "PodUniqKeyCmpFunc(%v, %v)", tt.args.i1, tt.args.i2)
		})
	}
}

func TestPodQoSCmpFunc(t *testing.T) {
	t.Parallel()

	pods := []*v1.Pod{
		makePodSorterTestPod("unknown", "unknown", ""),
		makePodSorterTestPod("system", apiconsts.PodAnnotationQoSLevelSystemCores, ""),
		makePodSorterTestPod("dedicated", apiconsts.PodAnnotationQoSLevelDedicatedCores, ""),
		makePodSorterTestPod("shared", apiconsts.PodAnnotationQoSLevelSharedCores, ""),
		makePodSorterTestPod("reclaimed", apiconsts.PodAnnotationQoSLevelReclaimedCores, ""),
	}

	sortedPods := NewPodSourceList(pods).Sort(PodQoSCmpFunc).Pods()

	assert.Equal(t, []string{"reclaimed", "shared", "dedicated", "system", "unknown"}, podNamesForSourceListTest(sortedPods))
}

func TestPodSaleModeCmpFunc(t *testing.T) {
	t.Parallel()

	pods := []*v1.Pod{
		makePodSorterTestPod("unknown", "", "default"),
		makePodSorterTestPod("reserved", "", apiconsts.PodSaleModeReserved),
		makePodSorterTestPod("scheduled", "", apiconsts.PodSaleModeScheduled),
		makePodSorterTestPod("spot", "", apiconsts.PodSaleModeSpot),
	}

	sortedPods := NewPodSourceList(pods).Sort(PodSaleModeCmpFunc, PodUniqKeyCmpFunc).Pods()

	assert.Equal(t, []string{"spot", "scheduled", "reserved", "unknown"}, podNamesForSourceListTest(sortedPods))
}

func TestNewPodSaleModeCmpFuncUsesCustomAnnotationKey(t *testing.T) {
	t.Parallel()

	customAnnotationKey := "custom.sale.mode"
	pods := []*v1.Pod{
		makePodSorterTestPod("default-key-spot", "", apiconsts.PodSaleModeSpot),
		{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "namespace-1",
				Name:      "custom-key-spot",
				Annotations: map[string]string{
					customAnnotationKey: apiconsts.PodSaleModeSpot,
				},
			},
		},
	}

	sortedPods := NewPodSourceList(pods).Sort(NewPodSaleModeCmpFunc(customAnnotationKey), PodUniqKeyCmpFunc).Pods()

	assert.Equal(t, []string{"custom-key-spot", "default-key-spot"}, podNamesForSourceListTest(sortedPods))
}

func TestNewPodSourceListCopiesPodsBeforeSort(t *testing.T) {
	t.Parallel()

	pods := []*v1.Pod{
		makePodForSourceListTest("pod-c"),
		makePodForSourceListTest("pod-a"),
		makePodForSourceListTest("pod-b"),
	}

	sortedPods := NewPodSourceList(pods).Sort(PodUniqKeyCmpFunc).Pods()

	assert.Equal(t, []string{"pod-c", "pod-b", "pod-a"}, podNamesForSourceListTest(sortedPods))
	assert.Equal(t, []string{"pod-c", "pod-a", "pod-b"}, podNamesForSourceListTest(pods))
}

func TestPodSourceListFilterCopiesPodsBeforeSort(t *testing.T) {
	t.Parallel()

	pods := []*v1.Pod{
		makePodForSourceListTest("pod-c"),
		makePodForSourceListTest("pod-a"),
		makePodForSourceListTest("pod-b"),
	}

	filteredPods := NewPodSourceList(pods).Filter(func(pod *v1.Pod) (bool, error) {
		return pod.Name != "pod-a", nil
	}).Sort(PodUniqKeyCmpFunc).TopN(1)

	assert.Equal(t, []string{"pod-c"}, podNamesForSourceListTest(filteredPods))
	assert.Equal(t, []string{"pod-c", "pod-a", "pod-b"}, podNamesForSourceListTest(pods))
}

func makePodForSourceListTest(name string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "namespace-1",
			Name:      name,
		},
	}
}

func makePodSorterTestPod(name, qosLevel, saleMode string) *v1.Pod {
	annotations := map[string]string{}
	if qosLevel != "" {
		annotations[apiconsts.PodAnnotationQoSLevelKey] = qosLevel
	}
	if saleMode != "" {
		annotations[apiconsts.PodAnnotationSaleModeKey] = saleMode
	}

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "namespace-1",
			Name:        name,
			Annotations: annotations,
		},
	}
}

func podNamesForSourceListTest(pods []*v1.Pod) []string {
	names := make([]string, 0, len(pods))
	for _, pod := range pods {
		names = append(names, pod.Name)
	}
	return names
}
