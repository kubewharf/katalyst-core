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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func makeRssOverusePlugin(conf *config.Configuration) (*RssOveruseEvictionPlugin, error) {
	metaServer := makeMetaServer()
	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	if err != nil {
		return nil, err
	}

	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}
	metaServer.MetricsFetcher = metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})

	return NewRssOveruseEvictionPlugin(nil, nil, metaServer, metrics.DummyMetrics{}, conf).(*RssOveruseEvictionPlugin), nil
}

func TestRssOveruseEvictionPlugin_GetEvictPods(t *testing.T) {
	plugin, err := makeRssOverusePlugin(makeConf())
	assert.NoError(t, err)
	assert.NotNil(t, plugin)

	pods := []*v1.Pod{
		// single container, has memory limit, no specified threshold
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-1",
				UID:  "001",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "container-1",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"memory": resource.MustParse("10Gi"),
							},
						},
					},
				},
			},
		},
		// single container, has no memory limit, no specified threshold
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-2",
				UID:  "002",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "container-1",
					},
				},
			},
		},
		// single container, has memory limit, has specified threshold
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-3",
				UID:  "003",
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementKey: "{\"rss_overuse_threshold\":\"0.8\"}",
				},
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "container-1",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"memory": resource.MustParse("10Gi"),
							},
						},
					},
				},
			},
		},
		// single container, has no memory limit, has specified threshold
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-4",
				UID:  "004",
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementKey: "{\"rss_overuse_threshold\":\"0.8\"}",
				},
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "container-1",
					},
				},
			},
		},
		// two containers,both has memory limit, has no specified threshold
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-5",
				UID:  "005",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "container-1",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"memory": resource.MustParse("10Gi"),
							},
						},
					},
					{
						Name: "container-2",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"memory": resource.MustParse("10Gi"),
							},
						},
					},
				},
			},
		},
		// two containers,one has no memory limit, has no specified threshold
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-6",
				UID:  "006",
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "container-1",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"memory": resource.MustParse("10Gi"),
							},
						},
					},
					{
						Name: "container-2",
					},
				},
			},
		},
		// two containers,both has memory limit, has specified threshold
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-7",
				UID:  "007",
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementKey: "{\"rss_overuse_threshold\":\"0.8\"}",
				},
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "container-1",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"memory": resource.MustParse("10Gi"),
							},
						},
					},
					{
						Name: "container-2",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"memory": resource.MustParse("4Gi"),
							},
						},
					},
				},
			},
		},
		// two containers,both has memory limit, has specified threshold
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "pod-8",
				UID:  "008",
				Annotations: map[string]string{
					apiconsts.PodAnnotationMemoryEnhancementKey: "{\"rss_overuse_threshold\":\"0.8\"}",
				},
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "container-1",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"memory": resource.MustParse("10Gi"),
							},
						},
					},
					{
						Name: "container-2",
						Resources: v1.ResourceRequirements{
							Limits: v1.ResourceList{
								"memory": resource.MustParse("4Gi"),
							},
						},
					},
				},
			},
		},
	}

	fakeMetricsFetcher := plugin.metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
	assert.NotNil(t, fakeMetricsFetcher)

	RssMetrics := [][]float64{
		{9 * 1024 * 1024 * 1024},
		{9 * 1024 * 1024 * 1024},
		{9 * 1024 * 1024 * 1024},
		{9 * 1024 * 1024 * 1024},
		{
			8 * 1024 * 1024 * 1024,
			8 * 1024 * 1024 * 1024,
		},
		{
			9 * 1024 * 1024 * 1024,
			1 * 1024 * 1024 * 1024,
		},
		{
			5 * 1024 * 1024 * 1024,
			2 * 1024 * 1024 * 1024,
		},
		{
			9 * 1024 * 1024 * 1024,
			3 * 1024 * 1024 * 1024,
		},
	}

	for i := range pods {
		for j := range pods[i].Spec.Containers {
			fakeMetricsFetcher.SetContainerMetric(string(pods[i].UID), pods[i].Spec.Containers[j].Name, consts.MetricMemRssContainer, RssMetrics[i][j])
		}
	}

	tests := []struct {
		name                       string
		enableRssOveruse           bool
		defaultRssOveruseThreshold float64
		wantedResult               sets.String
	}{
		{
			name:                       "disable rss overuse eviction",
			enableRssOveruse:           false,
			defaultRssOveruseThreshold: 0.1,
			wantedResult:               map[string]sets.Empty{},
		},
		{
			name:                       "enable rss overuse eviction, threshold is 1",
			enableRssOveruse:           true,
			defaultRssOveruseThreshold: 1,
			wantedResult: map[string]sets.Empty{
				"pod-3": {},
				"pod-8": {},
			},
		},
		{
			name:                       "enable rss overuse eviction, threshold is 0.1",
			enableRssOveruse:           true,
			defaultRssOveruseThreshold: 0.1,
			wantedResult: map[string]sets.Empty{
				"pod-1": {},
				"pod-3": {},
				"pod-5": {},
				"pod-8": {},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin.memoryEvictionPluginConfig.DynamicConf.SetEnableRssOveruseDetection(tt.enableRssOveruse)
			plugin.memoryEvictionPluginConfig.DynamicConf.SetRssOveruseRateThreshold(tt.defaultRssOveruseThreshold)

			evictPods, err2 := plugin.GetEvictPods(context.TODO(), &pluginapi.GetEvictPodsRequest{
				ActivePods: pods,
			})
			assert.NoError(t, err2)
			assert.Nil(t, evictPods.Condition)

			gotPods := sets.String{}
			for i := range evictPods.EvictPods {
				gotPods.Insert(evictPods.EvictPods[i].Pod.Name)
			}

			assert.Equal(t, tt.wantedResult, gotPods)
		})
	}
}
