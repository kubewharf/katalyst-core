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

package cpu

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubelet/config/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	evictionpluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
	consts2 "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/kubeletconfig"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var (
	highPriority int32 = 10000
	lowPriority  int32 = 100
)

func TestCmpKatalystQoS(t *testing.T) {
	t.Parallel()
	podList := []*v1.Pod{
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "p1",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelDedicatedCores,
				},
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "p2",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
				},
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "p3",
				Annotations: map[string]string{
					consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelReclaimedCores,
				},
			},
		},
	}
	conf := config.NewConfiguration()
	s := &SystemPressureEvictionPlugin{
		conf: conf,
	}

	general.NewMultiSorter(func(i1, i2 interface{}) int {
		return s.cmpKatalystQoS(i1.(*v1.Pod), i2.(*v1.Pod))
	}).Sort(native.NewPodSourceImpList(podList))

	res := make([]string, 0)
	for _, pod := range podList {
		res = append(res, pod.Name)
	}
	assert.Equal(t, []string{"p3", "p1", "p2"}, res)
}

func TestCmpSpecifiedLabels(t *testing.T) {
	t.Parallel()
	podList := []*v1.Pod{
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "p1",
				Labels: map[string]string{
					"testLabel": "label1",
				},
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "p3",
				Labels: map[string]string{
					"testLabel": "label3",
				},
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "p2",
				Labels: map[string]string{
					"testLabel": "label2",
				},
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name:   "p4",
				Labels: map[string]string{},
			},
		},
	}

	conf := config.NewConfiguration()
	conf.DynamicAgentConfiguration = dynamic.NewDynamicAgentConfiguration()
	conf.DynamicAgentConfiguration.SetDynamicConfiguration(
		&dynamic.Configuration{
			AdminQoSConfiguration: &adminqos.AdminQoSConfiguration{
				EvictionConfiguration: &eviction.EvictionConfiguration{
					CPUSystemPressureEvictionPluginConfiguration: &eviction.CPUSystemPressureEvictionPluginConfiguration{
						RankingLabels: map[string][]string{
							"testLabel": {
								"label3", "label2", "label1",
							},
						},
					},
				},
			},
		},
	)
	s := &SystemPressureEvictionPlugin{
		conf:        conf,
		dynamicConf: conf.DynamicAgentConfiguration,
	}
	general.NewMultiSorter(func(i1, i2 interface{}) int {
		return s.cmpSpecifiedLabels(i1.(*v1.Pod), i2.(*v1.Pod), "testLabel")
	}).Sort(native.NewPodSourceImpList(podList))

	res := make([]string, 0)
	for _, pod := range podList {
		res = append(res, pod.Name)
	}
	assert.Equal(t, []string{"p4", "p1", "p2", "p3"}, res)
}

func TestFilterGuaranteedPods(t *testing.T) {
	t.Parallel()

	podList := []*v1.Pod{
		{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Resources: v1.ResourceRequirements{
							Limits: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU:    resource.MustParse("4"),
								v1.ResourceMemory: resource.MustParse("8Gi"),
							},
							Requests: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU:    resource.MustParse("4"),
								v1.ResourceMemory: resource.MustParse("8Gi"),
							},
						},
					},
				},
			},
		},
		{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Name: "c1",
						Resources: v1.ResourceRequirements{
							Limits: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU:    resource.MustParse("4"),
								v1.ResourceMemory: resource.MustParse("8Gi"),
							},
							Requests: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU:    resource.MustParse("4"),
								v1.ResourceMemory: resource.MustParse("8Gi"),
							},
						},
					},
					{
						Name: "c2",
						Resources: v1.ResourceRequirements{
							Limits: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU: resource.MustParse("4"),
							},
							Requests: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU: resource.MustParse("4"),
							},
						},
					},
				},
			},
		},
		{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Resources: v1.ResourceRequirements{
							Limits: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU:    resource.MustParse("8"),
								v1.ResourceMemory: resource.MustParse("8Gi"),
							},
							Requests: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU:    resource.MustParse("8"),
								v1.ResourceMemory: resource.MustParse("8Gi"),
							},
						},
					},
				},
			},
		},
		{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Resources: v1.ResourceRequirements{
							Limits: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU: resource.MustParse("4"),
							},
							Requests: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU: resource.MustParse("4"),
							},
						},
					},
				},
			},
		},
		{
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{
						Resources: v1.ResourceRequirements{
							Limits: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU:    resource.MustParse("8"),
								v1.ResourceMemory: resource.MustParse("8Gi"),
							},
							Requests: map[v1.ResourceName]resource.Quantity{
								v1.ResourceCPU:    resource.MustParse("4"),
								v1.ResourceMemory: resource.MustParse("4Gi"),
							},
						},
					},
				},
			},
		},
	}

	podList, capacity, err := filterGuaranteedPods(podList, 32)
	assert.NoError(t, err)

	assert.Equal(t, 3, len(podList))
	assert.Equal(t, 20, capacity)
}

func TestThresholdMet(t *testing.T) {
	t.Parallel()

	cpuTopology, err := machine.GenerateDummyCPUTopology(16, 1, 2)
	require.NoError(t, err)

	for _, tc := range []struct {
		name            string
		metrics         map[string]map[string]float64
		podList         []*v1.Pod
		conf            *config.Configuration
		expectCondition *evictionpluginapi.Condition
		expectScope     string
		expectPodName   string
	}{
		{
			name: "not enable",
			conf: makeConf(false, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{}, -1, false, map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 0.5,
					"pod2": 1.5,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 1,
					"pod2": 2,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: nil,
			expectScope:     "",
			expectPodName:   "",
		},
		{
			name: "not met",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{}, -1, false, map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 0.5,
					"pod2": 1.5,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 1,
					"pod2": 2,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: nil,
			expectScope:     "",
			expectPodName:   "",
		},
		{
			name: "usage met soft without check CPU manager",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{}, -1, false, map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 0.5,
					"pod2": 1.5,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 4,
					"pod2": 5,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: &evictionpluginapi.Condition{
				ConditionType: 0,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: EvictionConditionSystemCPU,
				MetCondition:  true,
			},
			expectScope:   consts2.MetricCPUUsageContainer,
			expectPodName: "",
		},
		{
			name: "both usage and load met soft without check CPU manager",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{}, -1, false, map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 9,
					"pod2": 8,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 4,
					"pod2": 5,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: &evictionpluginapi.Condition{
				ConditionType: 0,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: EvictionConditionSystemCPU,
				MetCondition:  true,
			},
			expectScope:   consts2.MetricLoad1MinContainer,
			expectPodName: "",
		},
		{
			name: "both usage and load hard soft without check CPU manager",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{}, -1, false, map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 18,
					"pod2": 16,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 5,
					"pod2": 6,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: &evictionpluginapi.Condition{
				ConditionType: 0,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: EvictionConditionSystemCPU,
				MetCondition:  true,
			},
			expectScope:   consts2.MetricLoad1MinContainer,
			expectPodName: "pod1",
		},
		{
			name: "usage hard soft without check CPU manager, sort by QoS",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{eviction.FakeMetricNativeQoSLevel, eviction.FakeMetricPriority}, -1, false, map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 1,
					"pod2": 1,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 6,
					"pod2": 5,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("2"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: &evictionpluginapi.Condition{
				ConditionType: 0,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: EvictionConditionSystemCPU,
				MetCondition:  true,
			},
			expectScope:   consts2.MetricCPUUsageContainer,
			expectPodName: "pod2",
		},
		{
			name: "usage hard soft without check CPU manager, sort by priority",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{eviction.FakeMetricNativeQoSLevel, eviction.FakeMetricPriority}, -1, false, map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 1,
					"pod2": 1,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 6,
					"pod2": 5,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Priority: &highPriority,
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Priority: &lowPriority,
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: &evictionpluginapi.Condition{
				ConditionType: 0,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: EvictionConditionSystemCPU,
				MetCondition:  true,
			},
			expectScope:   consts2.MetricCPUUsageContainer,
			expectPodName: "pod2",
		},
		{
			name: "usage hard soft without check CPU manager, sort by custom metrics",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{eviction.FakeMetricNativeQoSLevel, eviction.FakeMetricPriority, consts2.MetricMemRssContainer}, -1, false, map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 1,
					"pod2": 1,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 6,
					"pod2": 5,
				},
				consts2.MetricMemRssContainer: {
					"pod1": 99,
					"pod2": 100,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: &evictionpluginapi.Condition{
				ConditionType: 0,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: EvictionConditionSystemCPU,
				MetCondition:  true,
			},
			expectScope:   consts2.MetricCPUUsageContainer,
			expectPodName: "pod2",
		},
		{
			name: "usage hard soft without check CPU manager, sort by labels",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{eviction.FakeMetricQoSLevel, eviction.FakeMetricNativeQoSLevel, eviction.FakeMetricPriority, "label.testLabel", consts2.MetricMemRssContainer}, -1, false,
				map[string][]string{
					"testLabel": {
						"label3", "label2", "label1",
					},
				}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 1,
					"pod2": 1,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 6,
					"pod2": 5,
				},
				consts2.MetricMemRssContainer: {
					"pod1": 99,
					"pod2": 100,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
						Labels: map[string]string{
							"testLabel": "label1",
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
						Labels: map[string]string{
							"testLabel": "label2",
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: &evictionpluginapi.Condition{
				ConditionType: 0,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: EvictionConditionSystemCPU,
				MetCondition:  true,
			},
			expectScope:   consts2.MetricCPUUsageContainer,
			expectPodName: "pod1",
		},
		{
			name: "usage hard soft without check CPU manager, sort by owner",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{eviction.FakeMetricOwnerLevel, eviction.FakeMetricNativeQoSLevel, eviction.FakeMetricPriority}, -1, false, map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 1,
					"pod2": 1,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 5,
					"pod2": 6,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
						OwnerReferences: []v12.OwnerReference{
							{
								Name: "test",
								Kind: "DaemonSet",
							},
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: &evictionpluginapi.Condition{
				ConditionType: 0,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: EvictionConditionSystemCPU,
				MetCondition:  true,
			},
			expectScope:   consts2.MetricCPUUsageContainer,
			expectPodName: "pod1",
		},
		{
			name: "not met with check CPU manager",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{eviction.FakeMetricNativeQoSLevel, eviction.FakeMetricPriority}, -1, true,
				map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 1,
					"pod2": 1,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 6,
					"pod2": 5,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("4"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: nil,
			expectScope:     "",
			expectPodName:   "",
		},
		{
			name: "hard met with check CPU manager",
			conf: makeConf(true, 2, 1, 0.6, 0.5,
				0.8, 10, 5*time.Minute, []string{eviction.FakeMetricNativeQoSLevel, eviction.FakeMetricPriority}, -1, true,
				map[string][]string{}),
			metrics: map[string]map[string]float64{
				consts2.MetricLoad1MinContainer: {
					"pod1": 17,
					"pod2": 17,
				},
				consts2.MetricCPUUsageContainer: {
					"pod1": 6,
					"pod2": 5,
				},
			},
			podList: []*v1.Pod{
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod1",
						UID:  "pod1",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
								Resources: v1.ResourceRequirements{
									Limits: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("8"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
									Requests: map[v1.ResourceName]resource.Quantity{
										v1.ResourceCPU:    resource.MustParse("8"),
										v1.ResourceMemory: resource.MustParse("8Gi"),
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: v12.ObjectMeta{
						Name: "pod2",
						UID:  "pod2",
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name: "fakeContainer",
							},
						},
					},
				},
			},
			expectCondition: &evictionpluginapi.Condition{
				ConditionType: 0,
				Effects:       []string{string(v1.TaintEffectNoSchedule)},
				ConditionName: EvictionConditionSystemCPU,
				MetCondition:  true,
			},
			expectScope:   consts2.MetricLoad1MinContainer,
			expectPodName: "pod2",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			metaServer := makeMetaServer(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}), cpuTopology)

			// set pod list
			metaServer.PodFetcher = &pod.PodFetcherStub{
				PodList: tc.podList,
			}
			fakeMetricsFetcher := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
			// set fake metrics
			for _, pod := range tc.podList {
				for metricName, podMetrics := range tc.metrics {
					if val, ok := podMetrics[string(pod.UID)]; ok {
						fakeMetricsFetcher.SetContainerMetric(string(pod.UID), "fakeContainer", metricName,
							utilmetric.MetricData{Value: val, Time: &now})
					}
				}
			}
			metaServer.MetricsFetcher = fakeMetricsFetcher
			metaServer.KubeletConfigFetcher = kubeletconfig.NewFakeKubeletConfigFetcher(v1beta1.KubeletConfiguration{
				CPUManagerPolicy: string(cpumanager.PolicyStatic),
			})

			// collect metrics
			ep := NewCPUSystemPressureEvictionPlugin(nil, nil, metaServer, nil, tc.conf).(*SystemPressureEvictionPlugin)
			for i := 0; i < tc.conf.DynamicAgentConfiguration.GetDynamicConfiguration().MetricRingSize; i++ {
				ep.collectMetrics(context.TODO())
			}

			// check threshold met
			resp, err := ep.ThresholdMet(context.TODO())
			require.NoError(t, err)
			if tc.expectCondition == nil {
				require.Nil(t, resp.Condition)
			} else {
				require.Equal(t, *(tc.expectCondition), *(resp.Condition))
			}
			require.Equal(t, tc.expectScope, resp.EvictionScope)

			if resp.MetType == 2 {
				podResp, err := ep.GetTopEvictionPods(context.TODO(), &evictionpluginapi.GetTopEvictionPodsRequest{
					ActivePods:    tc.podList,
					TopN:          1,
					EvictionScope: resp.EvictionScope,
				})
				require.NoError(t, err)
				assert.Equal(t, tc.expectPodName, podResp.TargetPods[0].Name)
			}
		})
	}
}

func TestName(t *testing.T) {
	t.Parallel()
	p := &SystemPressureEvictionPlugin{
		pluginName: EvictionPluginNameSystemCPUPressure,
	}
	assert.Equal(t, EvictionPluginNameSystemCPUPressure, p.Name())
}

func makeMetaServer(metricsFetcher metrictypes.MetricsFetcher, cpuTopology *machine.CPUTopology) *metaserver.MetaServer {
	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{},
	}

	metaServer.MetricsFetcher = metricsFetcher
	metaServer.KatalystMachineInfo = &machine.KatalystMachineInfo{
		CPUTopology: cpuTopology,
	}

	return metaServer
}

func makeConf(enable bool, systemLoadUpperBoundRatio, systemLoadLowerBoundRatio,
	systemUsageUpperBoundRatio, systemUsageLowerBoundRatio, thresholdMetPercentage float64,
	metricRingSize int, evictionCoolDownTime time.Duration, evictionRankingMetrics []string,
	gracePeriod int64, checkCPUManager bool, rankingLabelVals map[string][]string,
) *config.Configuration {
	conf := config.NewConfiguration()
	conf.GetDynamicConfiguration().EnableCPUSystemEviction = enable
	conf.GetDynamicConfiguration().SystemLoadUpperBoundRatio = systemLoadUpperBoundRatio
	conf.GetDynamicConfiguration().SystemLoadLowerBoundRatio = systemLoadLowerBoundRatio
	conf.GetDynamicConfiguration().SystemUsageUpperBoundRatio = systemUsageUpperBoundRatio
	conf.GetDynamicConfiguration().SystemUsageLowerBoundRatio = systemUsageLowerBoundRatio
	conf.GetDynamicConfiguration().CPUSystemPressureEvictionPluginConfiguration.ThresholdMetPercentage = thresholdMetPercentage
	conf.GetDynamicConfiguration().MetricRingSize = metricRingSize
	conf.GetDynamicConfiguration().EvictionCoolDownTime = evictionCoolDownTime
	conf.GetDynamicConfiguration().EvictionRankingMetrics = evictionRankingMetrics
	conf.GetDynamicConfiguration().CPUSystemPressureEvictionPluginConfiguration.GracePeriod = gracePeriod
	conf.GetDynamicConfiguration().CheckCPUManager = checkCPUManager
	conf.GetDynamicConfiguration().RankingLabels = rankingLabelVals
	return conf
}
