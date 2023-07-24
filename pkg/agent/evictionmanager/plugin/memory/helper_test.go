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

package memory

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func makeHelper(metaServer *metaserver.MetaServer) (*EvictionHelper, error) {
	conf := makeConf()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		return nil, err
	}

	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}
	metaServer.MetricsFetcher = metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

	return NewEvictionHelper(&metrics.DummyMetrics{}, metaServer, conf), nil
}

func TestEvictionHelper_getEvictionCmpFuncs(t *testing.T) {
	t.Parallel()

	metaServer := makeMetaServer()
	helper, err := makeHelper(metaServer)
	assert.NoError(t, err)
	assert.NotNil(t, helper)

	conf := makeConf()

	fakeMetricsFetcher := metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)

	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000001",
				Name: "pod-1",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &lowPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000002",
				Name: "pod-2",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &lowPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000003",
				Name: "pod-3",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &highPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000004",
				Name: "pod-4",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &highPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000005",
				Name: "pod-5",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &lowPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000006",
				Name: "pod-6",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &lowPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000007",
				Name: "pod-7",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &highPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				UID:  "000008",
				Name: "pod-8",
				Annotations: map[string]string{
					apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelSharedCores,
				},
			},
			Spec: v1.PodSpec{
				Priority: &highPriority,
				Containers: []v1.Container{
					{
						Name: "c",
					},
				},
			},
		},
	}

	podUsageSystem := []float64{
		5 * 1024 * 1024 * 1024,
		10 * 1024 * 1024 * 1024,
		20 * 1024 * 1024 * 1024,
		30 * 1024 * 1024 * 1024,
		40 * 1024 * 1024 * 1024,
		50 * 1024 * 1024 * 1024,
		60 * 1024 * 1024 * 1024,
		70 * 1024 * 1024 * 1024,
		80 * 1024 * 1024 * 1024,
	}

	now := time.Now()
	for i, pod := range pods {
		fakeMetricsFetcher.SetContainerMetric(string(pod.UID), pod.Spec.Containers[0].Name, consts.MetricMemUsageContainer, utilmetric.MetricData{Value: podUsageSystem[i], Time: &now})
	}
	general.NewMultiSorter(helper.getEvictionCmpFuncs(conf.GetDynamicConfiguration().SystemEvictionRankingMetrics,
		nonExistNumaID)...).Sort(native.NewPodSourceImpList(pods))

	wantPodNameList := []string{
		"pod-2",
		"pod-1",
		"pod-4",
		"pod-3",
		"pod-6",
		"pod-5",
		"pod-8",
		"pod-7",
	}
	for i := range pods {
		assert.Equal(t, wantPodNameList[i], pods[i].Name)
	}
}
