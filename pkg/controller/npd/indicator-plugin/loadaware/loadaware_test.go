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

package loadaware

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v12 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

func TestNewLoadAwarePlugin(t *testing.T) {
	t.Parallel()

	controlCtx, err := katalyst_base.GenerateFakeGenericContext()
	assert.NoError(t, err)

	conf := &controller.NPDConfig{
		LoadAwarePluginConfig: &controller.LoadAwarePluginConfig{
			Workers:                   3,
			PodUsageSelectorNamespace: "katalyst-system",
			PodUsageSelectorKey:       "app",
			PodUsageSelectorVal:       "testPod",
			MaxPodUsageCount:          10,
		},
	}

	updater := &fakeIndicatorUpdater{}

	p, err := NewLoadAwarePlugin(context.TODO(), conf, nil, controlCtx, updater)
	assert.NoError(t, err)
	assert.NotNil(t, p)
}

func TestRestoreNPD(t *testing.T) {
	t.Parallel()

	controlCtx, err := katalyst_base.GenerateFakeGenericContext()
	assert.NoError(t, err)

	p := &Plugin{
		npdLister: controlCtx.InternalInformerFactory.Node().V1alpha1().NodeProfileDescriptors().Lister(),
	}

	npds := []*v1alpha1.NodeProfileDescriptor{
		{
			ObjectMeta: v1.ObjectMeta{
				Name: "testNode1",
			},
			Status: v1alpha1.NodeProfileDescriptorStatus{
				NodeMetrics: []v1alpha1.ScopedNodeMetrics{
					{
						Scope: "testScope",
					},
					{
						Scope:   loadAwareMetricMetadataScope,
						Metrics: makeTestMetadata(4, 8*1024*1024*1024),
					},
				},
			},
		},
	}
	controlCtx.StartInformer(context.TODO())
	for _, npd := range npds {
		_, err = controlCtx.Client.InternalClient.NodeV1alpha1().NodeProfileDescriptors().
			Create(context.TODO(), npd, v1.CreateOptions{})
		assert.NoError(t, err)
	}
	time.Sleep(time.Second)
	p.restoreNPD()

	for _, npd := range npds {
		data, ok := p.nodeStatDataMap[npd.Name]
		assert.True(t, ok)

		assert.Equal(t, Avg15MinPointNumber, len(data.Latest15MinCache))
		assert.Equal(t, Max1HourPointNumber, len(data.Latest1HourCache))
		assert.Equal(t, Max1DayPointNumber, len(data.Latest1DayCache))
	}
}

func TestConstructNodeToPodMap(t *testing.T) {
	t.Parallel()

	controlCtx, err := katalyst_base.GenerateFakeGenericContext()
	assert.NoError(t, err)

	p := &Plugin{
		nodeToPodsMap: map[string]map[string]struct{}{},
		podLister:     controlCtx.KubeInformerFactory.Core().V1().Pods().Lister(),
	}

	pods := []*v12.Pod{
		{
			ObjectMeta: v1.ObjectMeta{
				Name:      "pod1",
				Namespace: "default",
			},
			Spec: v12.PodSpec{
				NodeName: "testNode1",
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{
				Name:      "pod2",
				Namespace: "default",
			},
			Spec: v12.PodSpec{
				NodeName: "testNode1",
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{
				Name:      "pod3",
				Namespace: "default",
			},
			Spec: v12.PodSpec{
				NodeName: "testNode2",
			},
		},
	}

	controlCtx.StartInformer(context.TODO())
	for _, pod := range pods {
		_, err = controlCtx.Client.KubeClient.CoreV1().Pods(pod.Namespace).
			Create(context.TODO(), pod, v1.CreateOptions{})
		assert.NoError(t, err)
	}
	time.Sleep(time.Second)

	p.constructNodeToPodMap()
	assert.Equal(t, 2, len(p.nodeToPodsMap))
	assert.Equal(t, 2, len(p.nodeToPodsMap["testNode1"]))
}

func TestWorker(t *testing.T) {
	t.Parallel()

	controlCtx, err := katalyst_base.GenerateFakeGenericContext()
	assert.NoError(t, err)

	p := &Plugin{
		nodeToPodsMap:      map[string]map[string]struct{}{},
		podLister:          controlCtx.KubeInformerFactory.Core().V1().Pods().Lister(),
		enableSyncPodUsage: false,
		npdUpdater:         &fakeIndicatorUpdater{},
		workers:            1,
		nodePoolMap: map[int32]sets.String{
			0: sets.NewString("node1", "node2", "node3"),
		},
		nodeStatDataMap: map[string]*NodeMetricData{},
		npdLister:       controlCtx.InternalInformerFactory.Node().V1alpha1().NodeProfileDescriptors().Lister(),
	}
	controlCtx.StartInformer(context.TODO())
	makeTestNodeStatData(p, "node1", 4, 16*1024*1024*1024)

	nodeMetrics := map[string]*v1beta1.NodeMetrics{
		"node1": {
			ObjectMeta: v1.ObjectMeta{
				Name: "node1",
			},
			Timestamp: v1.Time{Time: time.Now()},
			Usage: v12.ResourceList{
				v12.ResourceCPU:    resource.MustParse("4"),
				v12.ResourceMemory: resource.MustParse("6Gi"),
			},
		},
		"node2": {
			ObjectMeta: v1.ObjectMeta{
				Name: "node2",
			},
			Timestamp: v1.Time{Time: time.Now()},
			Usage: v12.ResourceList{
				v12.ResourceCPU:    resource.MustParse("4"),
				v12.ResourceMemory: resource.MustParse("6Gi"),
			},
		},
		"node3": {
			ObjectMeta: v1.ObjectMeta{
				Name: "node3",
			},
			Timestamp: v1.Time{Time: time.Now()},
			Usage: v12.ResourceList{
				v12.ResourceCPU:    resource.MustParse("2"),
				v12.ResourceMemory: resource.MustParse("5Gi"),
			},
		},
	}

	npds := []*v1alpha1.NodeProfileDescriptor{
		{
			ObjectMeta: v1.ObjectMeta{
				Name: "node1",
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{
				Name: "node2",
			},
		},
	}
	for i := range npds {
		_, err = controlCtx.Client.InternalClient.NodeV1alpha1().NodeProfileDescriptors().
			Create(context.TODO(), npds[i], v1.CreateOptions{})
		assert.NoError(t, err)
	}
	time.Sleep(time.Second)

	p.worker(0, nodeMetrics)
}

func TestTransferMetaToCRStore(t *testing.T) {
	t.Parallel()

	controlCtx, err := katalyst_base.GenerateFakeGenericContext()
	assert.NoError(t, err)
	updater := &fakeIndicatorUpdater{}

	p := &Plugin{
		npdLister:       controlCtx.InternalInformerFactory.Node().V1alpha1().NodeProfileDescriptors().Lister(),
		nodeStatDataMap: map[string]*NodeMetricData{},
		npdUpdater:      updater,
	}
	makeTestNodeStatData(p, "testNode1", 16, 32*1024*1024*1024)
	controlCtx.StartInformer(context.TODO())

	npds := []*v1alpha1.NodeProfileDescriptor{
		{
			ObjectMeta: v1.ObjectMeta{
				Name: "testNode1",
			},
		},
	}
	for _, npd := range npds {
		_, err = controlCtx.Client.InternalClient.NodeV1alpha1().NodeProfileDescriptors().
			Create(context.TODO(), npd, v1.CreateOptions{})
		assert.NoError(t, err)
	}
	time.Sleep(time.Second)

	p.transferMetaToCRStore()
	assert.NotNil(t, updater.data["testNode1"])
	assert.Equal(t, loadAwareMetricMetadataScope, updater.data["testNode1"].NodeMetrics[0].Scope)
	assert.Equal(t, 48+30+8, len(updater.data["testNode1"].NodeMetrics[0].Metrics))
}

func TestUpdatePodMetrics(t *testing.T) {
	t.Parallel()

	p := &Plugin{}

	npdStatus := &v1alpha1.NodeProfileDescriptorStatus{
		PodMetrics: []v1alpha1.ScopedPodMetrics{},
	}
	podUsage := map[string]v12.ResourceList{
		"default/testPod1": {
			v12.ResourceCPU:    resource.MustParse("2"),
			v12.ResourceMemory: resource.MustParse("6Gi"),
		},
		"default/testPod2": {
			v12.ResourceCPU:    resource.MustParse("3"),
			v12.ResourceMemory: resource.MustParse("8Gi"),
		},
		"default/testPod3": {
			v12.ResourceCPU:    resource.MustParse("1"),
			v12.ResourceMemory: resource.MustParse("6Gi"),
		},
		"default/testPod4": {
			v12.ResourceCPU:    resource.MustParse("2"),
			v12.ResourceMemory: resource.MustParse("5Gi"),
		},
		"default/testPod5": {
			v12.ResourceCPU:    resource.MustParse("4"),
			v12.ResourceMemory: resource.MustParse("6Gi"),
		},
		"default/testPod6": {
			v12.ResourceCPU:    resource.MustParse("7"),
			v12.ResourceMemory: resource.MustParse("13Gi"),
		},
	}

	p.updatePodMetrics(npdStatus, podUsage, 5)

	assert.Equal(t, 1, len(npdStatus.PodMetrics))
	assert.Equal(t, loadAwareMetricsScope, npdStatus.PodMetrics[0].Scope)
	assert.Equal(t, 5, len(npdStatus.PodMetrics[0].PodMetrics))
}

func TestCheckPodUsageRequired(t *testing.T) {
	t.Parallel()

	controlCtx, err := katalyst_base.GenerateFakeGenericContext()
	assert.NoError(t, err)

	p := &Plugin{
		podLister:                 controlCtx.KubeInformerFactory.Core().V1().Pods().Lister(),
		workers:                   3,
		podUsageSelectorNamespace: "katalyst-system",
		podUsageSelectorKey:       "app",
		podUsageSelectorVal:       "testPod",
	}
	controlCtx.StartInformer(context.TODO())

	pods := []*v12.Pod{
		{
			ObjectMeta: v1.ObjectMeta{
				Name:      "testPod",
				Namespace: "katalyst-system",
				Labels: map[string]string{
					"app": "testPod",
				},
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{
				Name:      "testPod2",
				Namespace: "default",
				Labels: map[string]string{
					"app": "testPod",
				},
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{
				Name:      "testPod3",
				Namespace: "katalyst-system",
				Labels: map[string]string{
					"app": "testPod3",
				},
			},
		},
	}
	for _, pod := range pods {
		_, err = controlCtx.Client.KubeClient.CoreV1().Pods(pod.Namespace).
			Create(context.TODO(), pod, v1.CreateOptions{})
		assert.NoError(t, err)
	}
	time.Sleep(time.Second)

	p.checkPodUsageRequired()
	assert.True(t, p.enableSyncPodUsage)

	err = controlCtx.Client.KubeClient.CoreV1().Pods("katalyst-system").Delete(context.TODO(), "testPod", v1.DeleteOptions{})
	assert.NoError(t, err)
	time.Sleep(time.Second)

	for i := 0; i < 10; i++ {
		p.checkPodUsageRequired()
	}
	assert.False(t, p.enableSyncPodUsage)
}

func TestReCleanPodData(t *testing.T) {
	t.Parallel()

	controlCtx, err := katalyst_base.GenerateFakeGenericContext()
	assert.NoError(t, err)

	p := &Plugin{
		podLister: controlCtx.KubeInformerFactory.Core().V1().Pods().Lister(),
		podStatDataMap: map[string]*PodMetricData{
			"default/testPod1": nil,
			"default/testPod2": nil,
			"default/testPod3": nil,
		},
	}
	controlCtx.StartInformer(context.TODO())

	pods := []*v12.Pod{
		{
			ObjectMeta: v1.ObjectMeta{
				Name:      "testPod1",
				Namespace: "default",
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{
				Name:      "testPod5",
				Namespace: "default",
			},
		},
	}
	for _, pod := range pods {
		_, err = controlCtx.Client.KubeClient.CoreV1().Pods(pod.Namespace).
			Create(context.TODO(), pod, v1.CreateOptions{})
		assert.NoError(t, err)
	}
	time.Sleep(time.Second)

	p.reCleanPodData()
	assert.Equal(t, 1, len(p.podStatDataMap))
}

func TestName(t *testing.T) {
	t.Parallel()
	p := &Plugin{}
	assert.Equal(t, LoadAwarePluginName, p.Name())
}

func TestGetSupportedNodeMetricsScope(t *testing.T) {
	t.Parallel()
	p := Plugin{}
	assert.Equal(t, []string{loadAwareMetricsScope, loadAwareMetricMetadataScope}, p.GetSupportedNodeMetricsScope())
	assert.Equal(t, []string{loadAwareMetricsScope}, p.GetSupportedPodMetricsScope())
}

func makeTestMetadata(cpu, memory int64) []v1alpha1.MetricValue {
	res := make([]v1alpha1.MetricValue, 0)
	now := time.Now()
	rand.Seed(now.Unix())
	for i := 0; i < Avg15MinPointNumber; i++ {
		res = append(res, v1alpha1.MetricValue{
			MetricName: "cpu",
			Timestamp:  v1.Time{Time: now},
			Window:     &v1.Duration{Duration: 15 * time.Minute},
			Value:      *resource.NewQuantity(rand.Int63nRange(0, cpu), resource.DecimalSI),
		})
		res = append(res, v1alpha1.MetricValue{
			MetricName: "memory",
			Timestamp:  v1.Time{Time: now},
			Window:     &v1.Duration{Duration: 15 * time.Minute},
			Value:      *resource.NewQuantity(rand.Int63nRange(0, memory), resource.BinarySI),
		})
		now = now.Add(time.Minute)
	}

	for i := 0; i < Max1HourPointNumber; i++ {
		res = append(res, v1alpha1.MetricValue{
			MetricName: "cpu",
			Timestamp:  v1.Time{Time: now},
			Window:     &v1.Duration{Duration: time.Hour},
			Value:      *resource.NewQuantity(rand.Int63nRange(0, cpu), resource.DecimalSI),
		})
		res = append(res, v1alpha1.MetricValue{
			MetricName: "memory",
			Timestamp:  v1.Time{Time: now},
			Window:     &v1.Duration{Duration: time.Hour},
			Value:      *resource.NewQuantity(rand.Int63nRange(0, memory), resource.BinarySI),
		})
		now = now.Add(time.Minute)
	}

	for i := 0; i < Max1DayPointNumber; i++ {
		res = append(res, v1alpha1.MetricValue{
			MetricName: "cpu",
			Timestamp:  v1.Time{Time: now},
			Window:     &v1.Duration{Duration: 24 * time.Hour},
			Value:      *resource.NewQuantity(rand.Int63nRange(0, cpu), resource.DecimalSI),
		})
		res = append(res, v1alpha1.MetricValue{
			MetricName: "memory",
			Timestamp:  v1.Time{Time: now},
			Window:     &v1.Duration{Duration: 24 * time.Hour},
			Value:      *resource.NewQuantity(rand.Int63nRange(0, memory), resource.BinarySI),
		})
		now = now.Add(time.Minute)
	}

	return res
}

func makeTestNodeStatData(plugin *Plugin, nodeName string, cpu, memory int64) {
	if plugin.nodeStatDataMap == nil {
		plugin.nodeStatDataMap = map[string]*NodeMetricData{}
	}
	if plugin.nodeStatDataMap[nodeName] == nil {
		plugin.nodeStatDataMap[nodeName] = &NodeMetricData{}
	}
	now := time.Now().Add(-2 * time.Hour)
	rand.Seed(now.Unix())

	for i := 0; i < Avg15MinPointNumber; i++ {
		plugin.nodeStatDataMap[nodeName].Latest15MinCache = append(plugin.nodeStatDataMap[nodeName].Latest15MinCache, v12.ResourceList{
			v12.ResourceCPU:    *resource.NewQuantity(rand.Int63nRange(0, cpu), resource.DecimalSI),
			v12.ResourceMemory: *resource.NewQuantity(rand.Int63nRange(0, memory), resource.BinarySI),
		})
	}

	for i := 0; i < Max1HourPointNumber; i++ {
		plugin.nodeStatDataMap[nodeName].Latest1HourCache = append(plugin.nodeStatDataMap[nodeName].Latest1HourCache, &ResourceListWithTime{
			Ts: now.Unix(),
			ResourceList: v12.ResourceList{
				v12.ResourceCPU:    *resource.NewQuantity(rand.Int63nRange(0, cpu), resource.DecimalSI),
				v12.ResourceMemory: *resource.NewQuantity(rand.Int63nRange(0, memory), resource.BinarySI),
			},
		})
		now = now.Add(time.Minute)
	}

	for i := 0; i < Max1DayPointNumber; i++ {
		plugin.nodeStatDataMap[nodeName].Latest1DayCache = append(plugin.nodeStatDataMap[nodeName].Latest1DayCache, &ResourceListWithTime{
			Ts: now.Unix(),
			ResourceList: v12.ResourceList{
				v12.ResourceCPU:    *resource.NewQuantity(rand.Int63nRange(0, cpu), resource.DecimalSI),
				v12.ResourceMemory: *resource.NewQuantity(rand.Int63nRange(0, memory), resource.BinarySI),
			},
		})
		now = now.Add(time.Minute)
	}
}

type fakeIndicatorUpdater struct {
	data map[string]v1alpha1.NodeProfileDescriptorStatus
}

func (f *fakeIndicatorUpdater) UpdateNodeMetrics(name string, scopedNodeMetrics []v1alpha1.ScopedNodeMetrics) {
	if f.data == nil {
		f.data = map[string]v1alpha1.NodeProfileDescriptorStatus{}
	}
	data, ok := f.data[name]
	if !ok {
		data = v1alpha1.NodeProfileDescriptorStatus{}
	}
	data.NodeMetrics = scopedNodeMetrics
	f.data[name] = data
}

func (f *fakeIndicatorUpdater) UpdatePodMetrics(nodeName string, scopedPodMetrics []v1alpha1.ScopedPodMetrics) {
	if f.data == nil {
		f.data = map[string]v1alpha1.NodeProfileDescriptorStatus{}
	}
	data, ok := f.data[nodeName]
	if !ok {
		data = v1alpha1.NodeProfileDescriptorStatus{}
	}
	data.PodMetrics = scopedPodMetrics
	f.data[nodeName] = data
}
