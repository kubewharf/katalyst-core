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

package rootfs

import (
	"context"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	evictionapi "k8s.io/kubernetes/pkg/kubelet/eviction/api"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	gi = 1024 * 1024 * 1024
)

var (
	thresholdValue50Gi  = resource.MustParse("50Gi")
	thresholdValue100Gi = resource.MustParse("100Gi")
	thresholdValue200Gi = resource.MustParse("200Gi")
)

type fakeConf struct {
	enableRootfsOveruseEviction        bool
	rootfsOveruseEvictionCount         int
	sharedQoSRootfsOveruseThreshold    *evictionapi.ThresholdValue
	reclaimedQoSRootfsOveruseThreshold *evictionapi.ThresholdValue
	supportedQosLevels                 []string
	sharedQoSNamespaceFilter           []string
}

func generateTestConfiguration(fc *fakeConf) *config.Configuration {
	testConfiguration := config.NewConfiguration()
	testConfiguration.GetDynamicConfiguration().EnableRootfsOveruseEviction = fc.enableRootfsOveruseEviction
	testConfiguration.GetDynamicConfiguration().RootfsOveruseEvictionCount = fc.rootfsOveruseEvictionCount
	testConfiguration.GetDynamicConfiguration().SharedQoSRootfsOveruseThreshold = fc.sharedQoSRootfsOveruseThreshold
	testConfiguration.GetDynamicConfiguration().ReclaimedQoSRootfsOveruseThreshold = fc.reclaimedQoSRootfsOveruseThreshold
	testConfiguration.GetDynamicConfiguration().RootfsOveruseEvictionSupportedQoSLevels = fc.supportedQosLevels
	testConfiguration.GetDynamicConfiguration().SharedQoSNamespaceFilter = fc.sharedQoSNamespaceFilter
	return testConfiguration
}

func newTestOverusePlugin(fc *fakeConf, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) *PodRootfsOveruseEvictionPlugin {
	conf := generateTestConfiguration(fc)
	return NewPodRootfsOveruseEvictionPlugin(nil, nil, metaServer, emitter, conf).(*PodRootfsOveruseEvictionPlugin)
}

func generateTestPods(uid, ns, containerName, qosLevel string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:         types.UID(uid),
			Namespace:   ns,
			Annotations: map[string]string{"katalyst.kubewharf.io/qos_level": qosLevel},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: containerName,
				},
			},
		},
	}
}

func TestRootfsOveruseEvictionPlugin_GetEvictPods(t *testing.T) {
	t.Parallel()

	emitter := metrics.DummyMetrics{}
	fakeFetcher := metric.NewFakeMetricsFetcher(emitter).(*metric.FakeMetricsFetcher)
	fakeFetcher.SetNodeMetric(consts.MetricsImageFsAvailable, utilmetric.MetricData{Value: 2100 * gi})
	fakeFetcher.SetNodeMetric(consts.MetricsImageFsUsed, utilmetric.MetricData{Value: 900 * gi})
	fakeFetcher.SetNodeMetric(consts.MetricsImageFsCapacity, utilmetric.MetricData{Value: 3000 * gi})

	fakeFetcher.SetContainerMetric("podUID1", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: float64(100 * gi)})
	fakeFetcher.SetContainerMetric("podUID2", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: float64(200 * gi)})
	fakeFetcher.SetContainerMetric("podUID3", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: float64(300 * gi)})
	fakeFetcher.SetContainerMetric("podUID4", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: float64(400 * gi)})
	fakeFetcher.SetContainerMetric("podUID5", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: float64(500 * gi)})
	fakeFetcher.SetContainerMetric("podUID6", "containerName", consts.MetricsContainerRootfsUsed, utilmetric.MetricData{Value: float64(600 * gi)})

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			MetricsFetcher: fakeFetcher,
		},
	}

	activePods := []*v1.Pod{
		// shared cores podUID1
		generateTestPods("podUID1", "default", "containerName", apiconsts.PodAnnotationQoSLevelSharedCores),
		// reclaimed cores podUID2
		generateTestPods("podUID2", "", "containerName", apiconsts.PodAnnotationQoSLevelReclaimedCores),
		// shared cores podUID3
		generateTestPods("podUID3", "test", "containerName", apiconsts.PodAnnotationQoSLevelSharedCores),
		// reclaimed cores podUID4
		generateTestPods("podUID4", "", "containerName", apiconsts.PodAnnotationQoSLevelReclaimedCores),
		// shared cores podUID5
		generateTestPods("podUID5", "default", "containerName", apiconsts.PodAnnotationQoSLevelSharedCores),
		// dedicated cores podUID6
		generateTestPods("podUID6", "", "containerName", apiconsts.PodAnnotationQoSLevelDedicatedCores),
	}

	testCases := []struct {
		name                               string
		enableRootfsOveruseEviction        bool
		sharedQoSRootfsOveruseThreshold    *evictionapi.ThresholdValue
		reclaimedQoSRootfsOveruseThreshold *evictionapi.ThresholdValue
		rootfsOveruseEvictionCount         int
		supportedQosLevels                 []string
		sharedQoSNamespaceFilter           []string
		expectResult                       []string
	}{
		{
			name:                        "disable rootfs overuse eviction",
			enableRootfsOveruseEviction: false,
			expectResult:                []string{},
		},
		{
			name:                        "enable rootfs overuse eviction, zero evict count",
			enableRootfsOveruseEviction: true,
			rootfsOveruseEvictionCount:  0,
			expectResult:                []string{},
		},
		{
			name:                        "enable rootfs overuse eviction, support dedicated cores",
			enableRootfsOveruseEviction: true,
			rootfsOveruseEvictionCount:  1,
			supportedQosLevels:          []string{apiconsts.PodAnnotationQoSLevelDedicatedCores},
			expectResult:                []string{},
		},
		{
			name:                        "enable rootfs overuse eviction, only supported shared cores, no threshold",
			enableRootfsOveruseEviction: true,
			rootfsOveruseEvictionCount:  1,
			supportedQosLevels:          []string{apiconsts.PodAnnotationQoSLevelSharedCores},
			sharedQoSNamespaceFilter:    []string{"default", "test"},
			expectResult:                []string{},
		},
		{
			name:                        "enable rootfs overuse eviction, only supported shared cores, shared threshold 50Gi, limit evict count 1",
			enableRootfsOveruseEviction: true,
			rootfsOveruseEvictionCount:  1,
			supportedQosLevels:          []string{apiconsts.PodAnnotationQoSLevelSharedCores},
			sharedQoSNamespaceFilter:    []string{"default", "test"},
			sharedQoSRootfsOveruseThreshold: &evictionapi.ThresholdValue{
				Quantity:   &thresholdValue50Gi,
				Percentage: 0,
			},
			expectResult: []string{"podUID5"},
		},
		{
			name:                        "enable rootfs overuse eviction, only supported shared cores, shared threshold 50Gi",
			enableRootfsOveruseEviction: true,
			rootfsOveruseEvictionCount:  10,
			supportedQosLevels:          []string{apiconsts.PodAnnotationQoSLevelSharedCores},
			sharedQoSNamespaceFilter:    []string{"default", "test"},
			sharedQoSRootfsOveruseThreshold: &evictionapi.ThresholdValue{
				Quantity:   &thresholdValue50Gi,
				Percentage: 0,
			},
			expectResult: []string{"podUID5", "podUID3", "podUID1"},
		},
		{
			name:                        "enable rootfs overuse eviction, only supported shared cores, shared threshold 50Gi, namespace filter",
			enableRootfsOveruseEviction: true,
			rootfsOveruseEvictionCount:  10,
			supportedQosLevels:          []string{apiconsts.PodAnnotationQoSLevelSharedCores},
			sharedQoSRootfsOveruseThreshold: &evictionapi.ThresholdValue{
				Quantity:   &thresholdValue50Gi,
				Percentage: 0,
			},
			sharedQoSNamespaceFilter: []string{"default"},
			expectResult:             []string{"podUID5", "podUID1"},
		},
		{
			name:                        "enable rootfs overuse eviction, only supported reclaimed cores, reclaimed threshold 100Gi",
			enableRootfsOveruseEviction: true,
			rootfsOveruseEvictionCount:  10,
			supportedQosLevels:          []string{apiconsts.PodAnnotationQoSLevelReclaimedCores},
			reclaimedQoSRootfsOveruseThreshold: &evictionapi.ThresholdValue{
				Quantity:   &thresholdValue100Gi,
				Percentage: 0,
			},
			expectResult: []string{"podUID4", "podUID2"},
		},
		{
			name:                        "enable rootfs overuse eviction, 100Gi shared and 200Gi reclaimed cores",
			enableRootfsOveruseEviction: true,
			rootfsOveruseEvictionCount:  10,
			supportedQosLevels:          []string{apiconsts.PodAnnotationQoSLevelSharedCores, apiconsts.PodAnnotationQoSLevelReclaimedCores},
			sharedQoSNamespaceFilter:    []string{"default", "test"},
			sharedQoSRootfsOveruseThreshold: &evictionapi.ThresholdValue{
				Quantity:   &thresholdValue100Gi,
				Percentage: 0,
			},
			reclaimedQoSRootfsOveruseThreshold: &evictionapi.ThresholdValue{
				Quantity:   &thresholdValue200Gi,
				Percentage: 0,
			},
			expectResult: []string{"podUID5", "podUID4", "podUID3"},
		},
		{
			name:                        "enable rootfs overuse eviction, 0.15 shared and 0.1 reclaimed cores, limit evict count 2",
			enableRootfsOveruseEviction: true,
			rootfsOveruseEvictionCount:  2,
			supportedQosLevels:          []string{apiconsts.PodAnnotationQoSLevelSharedCores, apiconsts.PodAnnotationQoSLevelReclaimedCores},
			sharedQoSNamespaceFilter:    []string{"default", "test"},
			sharedQoSRootfsOveruseThreshold: &evictionapi.ThresholdValue{
				Quantity:   nil,
				Percentage: 0.15,
			},
			reclaimedQoSRootfsOveruseThreshold: &evictionapi.ThresholdValue{
				Quantity:   nil,
				Percentage: 0.1,
			},
			expectResult: []string{"podUID5", "podUID4"},
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fc := &fakeConf{
				enableRootfsOveruseEviction:        tc.enableRootfsOveruseEviction,
				sharedQoSRootfsOveruseThreshold:    tc.sharedQoSRootfsOveruseThreshold,
				reclaimedQoSRootfsOveruseThreshold: tc.reclaimedQoSRootfsOveruseThreshold,
				rootfsOveruseEvictionCount:         tc.rootfsOveruseEvictionCount,
				supportedQosLevels:                 tc.supportedQosLevels,
				sharedQoSNamespaceFilter:           tc.sharedQoSNamespaceFilter,
			}
			plugin := newTestOverusePlugin(fc, metaServer, emitter)
			resp, err := plugin.GetEvictPods(context.Background(), &pluginapi.GetEvictPodsRequest{
				ActivePods: activePods,
			})
			if err != nil {
				t.Errorf("GetEvictPods failed, err: %v", err)
			}
			if len(resp.EvictPods) != len(tc.expectResult) {
				t.Errorf("GetEvictPods failed, expect result: %v, got: %v", tc.expectResult, resp.EvictPods)
			}
			for i, pod := range resp.EvictPods {
				if string(pod.Pod.UID) != tc.expectResult[i] {
					t.Errorf("GetEvictPods failed, expect result: %v, got: %v", tc.expectResult, resp.EvictPods)
				}
			}
		})
	}
}
