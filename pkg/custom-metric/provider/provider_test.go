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

package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/metrics/pkg/apis/custom_metrics"
	"k8s.io/metrics/pkg/apis/external_metrics"
	"sigs.k8s.io/custom-metrics-apiserver/pkg/provider"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	metricconf "github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/remote"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func generateStorePodMeta(namespace, name, nameLabel string, port int32) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Pod",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				"test": "local-store",
				"name": nameLabel,
			},
		},
	}
}

func generateStoreNodeMeta(name, nameLabel string) *metav1.PartialObjectMetadata {
	return &metav1.PartialObjectMetadata{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "Node",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"name": nameLabel,
			},
		},
	}
}

func generateStorePod(namespace, name, nameLabel string, port int32) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				"test": "local-store",
				"name": nameLabel,
			},
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name: "pod-container",
					Ports: []v1.ContainerPort{
						{
							Name:     native.ContainerMetricStorePortName,
							HostPort: port,
						},
					},
				},
			},
		},
		Status: v1.PodStatus{
			HostIP: "127.0.0.1",
			ContainerStatuses: []v1.ContainerStatus{
				{
					Ready: true,
				},
			},
		},
	}
}

func TestWithLocalStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	p1 := generateStorePodMeta("ns-1", "pod-1", "full_metric_with_conflict_time", 11)
	p2 := generateStorePodMeta("ns-2", "pod-2", "full_metric_with_multiple_data", 11)
	n1 := generateStoreNodeMeta("node-1", "full_metric_with_node")

	baseCtx, err := katalystbase.GenerateFakeGenericContext(nil, nil, nil, []runtime.Object{p1, p2, n1})
	assert.NoError(t, err)

	genericConf := &metricconf.GenericMetricConfiguration{
		OutOfDataPeriod: time.Second * 20,
	}
	storeConf := &metricconf.StoreConfiguration{
		ServiceDiscoveryConf: &generic.ServiceDiscoveryConf{
			PodSinglePortSDConf: &generic.PodSinglePortSDConf{
				PortName: native.ContainerMetricStorePortName,
				PodLister: labels.SelectorFromSet(map[string]string{
					"test": "local-store",
				}),
			},
		},
		IndexLabelKeys: []string{"name"},
	}

	s, err := local.NewLocalMemoryMetricStore(ctx, baseCtx, genericConf, storeConf)
	assert.NoError(t, err)

	baseCtx.StartInformer(ctx)
	err = s.Start()
	assert.NoError(t, err)

	p := NewMetricProviderImp(ctx, baseCtx, s)
	testProvider(t, p, s, ctx, baseCtx, genericConf, storeConf)
}

func TestWithRemoteStoreOne(t *testing.T) {
	t.Parallel()

	testWithRemoteStoreWithIndex(t, []int{1})
}

func TestWithRemoteStoreTwo(t *testing.T) {
	t.Parallel()

	testWithRemoteStoreWithIndex(t, []int{1, 2})
}

func TestWithRemoteStoreThree(t *testing.T) {
	t.Parallel()

	testWithRemoteStoreWithIndex(t, []int{1, 2, 3})
}

func testWithRemoteStoreWithIndex(t *testing.T, index []int) {
	ctx := context.Background()

	genericConf := &metricconf.GenericMetricConfiguration{
		OutOfDataPeriod: time.Second * 20,
	}
	storeConf := &metricconf.StoreConfiguration{
		ServiceDiscoveryConf: &generic.ServiceDiscoveryConf{
			PodSinglePortSDConf: &generic.PodSinglePortSDConf{
				PortName: native.ContainerMetricStorePortName,
				PodLister: labels.SelectorFromSet(map[string]string{
					"test": "local-store",
				}),
			},
		},
		StoreServerReplicaTotal: len(index),
		GCPeriod:                time.Second,
		PurgePeriod:             time.Second,
		IndexLabelKeys:          []string{"name"},
	}

	lp1 := generateStorePodMeta("ns-1", "pod-1", "full_metric_with_conflict_time", 11)
	lp2 := generateStorePodMeta("ns-2", "pod-2", "full_metric_with_multiple_data", 22)
	ln1 := generateStoreNodeMeta("node-1", "full_metric_with_node")

	var podList []runtime.Object
	for i := range index {
		mux := http.NewServeMux()

		server := httptest.NewServer(mux)
		t.Logf("server url %v\n", server.URL)

		urlList := strings.Split(server.URL, ":")
		assert.Equal(t, len(urlList), 3)
		port, err := strconv.Atoi(strings.TrimSpace(urlList[2]))
		assert.NoError(t, err)

		baseCtx, err := katalystbase.GenerateFakeGenericContext(nil, nil, nil, []runtime.Object{lp1, lp2, ln1})
		assert.NoError(t, err)
		baseCtx.Handler = mux

		l, err := local.NewLocalMemoryMetricStore(ctx, baseCtx, genericConf, storeConf)
		assert.NoError(t, err)
		baseCtx.StartInformer(ctx)

		err = l.Start()
		assert.NoError(t, err)
		l.(*local.LocalMemoryMetricStore).Serve(baseCtx.Handler.(*http.ServeMux))

		p := generateStorePod("ns-fake", fmt.Sprintf("pod-r-%v", i), fmt.Sprintf("pod-r-%v", i), int32(port))
		podList = append(podList, p)
	}

	baseCtx, err := katalystbase.GenerateFakeGenericContext(podList, nil, nil)
	assert.NoError(t, err)

	r, err := remote.NewRemoteMemoryMetricStore(ctx, baseCtx, genericConf, storeConf)
	assert.NoError(t, err)
	baseCtx.StartInformer(ctx)

	err = r.Start()
	assert.NoError(t, err)

	p := NewMetricProviderImp(ctx, baseCtx, r)
	testProvider(t, p, r, ctx, baseCtx, genericConf, storeConf)
}

func testProvider(t *testing.T, p MetricProvider, s store.MetricStore, ctx context.Context, baseCtx *katalystbase.GenericContext,
	genericConf *metricconf.GenericMetricConfiguration, storeConf *metricconf.StoreConfiguration) {
	var err error

	podGR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}.GroupResource()
	nodeGR := schema.GroupVersionResource{Version: "v1", Resource: "nodes"}.GroupResource()

	now := time.Now().Add(time.Second * 10)
	err = s.InsertMetric([]*data.MetricSeries{
		{
			Name: "none_namespace_metric",
			Labels: map[string]string{
				"labels-1":      "key-1",
				"selector_name": "none_namespace_metric",
			},
			Series: []*data.MetricData{
				{
					Data:      1,
					Timestamp: now.UnixMilli(),
				},
				{
					Data:      2,
					Timestamp: now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() + time.Second.Milliseconds()*5,
				},
			},
		},
		{
			Name: "none_namespace_metric_all_timeout",
			Labels: map[string]string{
				"labels-2":      "key-2",
				"selector_name": "none_namespace_metric_all_timeout",
			},
			Series: []*data.MetricData{
				{
					Data:      2,
					Timestamp: now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() + time.Second.Milliseconds()*5,
				},
			},
		},
		{
			Name: "none_object_metric",
			Labels: map[string]string{
				"selector_name": "none_object_metric",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyNamespace): "ns-1",
			},
			Series: []*data.MetricData{
				{
					Data:      19,
					Timestamp: now.UnixMilli(),
				},
				{
					Data:      23,
					Timestamp: now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds(),
				},
			},
		},
		{
			Name: "invalid_object_metric",
			Labels: map[string]string{
				"selector_name": "invalid_object_metric",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyNamespace): "ns-1",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObject):    "pods",
			},
			Series: []*data.MetricData{
				{
					Data:      19,
					Timestamp: now.UnixMilli(),
				},
			},
		},
		{
			Name: "object_metric_with_unsupported_obj",
			Labels: map[string]string{
				"selector_name": "object_metric_with_unsupported_obj",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyNamespace):  "ns-1",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObject):     "deployment",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObjectName): "dp-1",
			},
			Series: []*data.MetricData{
				{
					Data:      31,
					Timestamp: now.UnixMilli(),
				},
			},
		},
		{
			Name: "full_metric_with_conflict_time",
			Labels: map[string]string{
				"selector_name": "full_metric_with_conflict_time",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyNamespace):  "ns-1",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObject):     "pods",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObjectName): "pod-1",
			},
			Series: []*data.MetricData{
				{
					Data:      31,
					Timestamp: now.UnixMilli(),
				},
				{
					Data:      21,
					Timestamp: now.UnixMilli(),
				},
			},
		},
		{
			Name: "full_metric_with_multiple_data",
			Labels: map[string]string{
				"selector_name": "full_metric_with_multiple_data",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyNamespace):  "ns-2",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObject):     "pods",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObjectName): "pod-2",
			},
			Series: []*data.MetricData{
				{
					Data:      31,
					Timestamp: now.UnixMilli(),
				},
				{
					Data:      44,
					Timestamp: now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() - time.Second.Milliseconds()*3,
				},
			},
		},
		{
			Name: "full_metric_with_multiple_data",
			Labels: map[string]string{
				"selector_name": "full_metric_with_multiple_data",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyNamespace):  "ns-2",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObject):     "pods",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObjectName): "pod-2",
			},
			Series: []*data.MetricData{
				{
					Data:      34,
					Timestamp: now.UnixMilli(),
				},
			},
		},
		{
			Name: "full_metric_with_multiple_data",
			Labels: map[string]string{
				"selector_name": "full_metric_with_multiple_data",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyNamespace):  "ns-2",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObject):     "pods",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObjectName): "pod-2",
			},
			Series: []*data.MetricData{
				{
					Data:      86,
					Timestamp: now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() - time.Second.Milliseconds()*2,
				},
			},
		},
		{
			Name: "full_metric_with_node",
			Labels: map[string]string{
				"selector_name": "full_metric_with_node",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObject):     "nodes",
				fmt.Sprintf("%v", data.CustomMetricLabelKeyObjectName): "node-1",
			},
			Series: []*data.MetricData{
				{
					Data:      73,
					Timestamp: now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() - time.Second.Milliseconds()*2,
				},
			},
		},
	})
	assert.NoError(t, err)

	var (
		oneMetric   *custom_metrics.MetricValue
		batchMetric *custom_metrics.MetricValueList
		metricInfo  []provider.CustomMetricInfo

		batchExternal *external_metrics.ExternalMetricValueList
		externalInfo  []provider.ExternalMetricInfo
	)

	t.Log("#### 1.1: ListAllMetrics")

	metricInfo = p.ListAllMetrics()
	assert.Equal(t, 3, len(metricInfo))
	assert.ElementsMatch(t, []provider.CustomMetricInfo{
		{
			GroupResource: podGR,
			Namespaced:    true,
			Metric:        "full_metric_with_conflict_time",
		},
		{
			GroupResource: podGR,
			Namespaced:    true,
			Metric:        "full_metric_with_multiple_data",
		},
		{
			GroupResource: nodeGR,
			Namespaced:    false,
			Metric:        "full_metric_with_node",
		},
	}, metricInfo)

	t.Log("#### 1.2.1: GetMetricByName for node")

	oneMetric, err = p.GetMetricByName(ctx, types.NamespacedName{
		Name: "node-1",
	}, provider.CustomMetricInfo{
		GroupResource: nodeGR,
		Namespaced:    false,
		Metric:        "full_metric_with_node",
	}, labels.Everything())
	assert.NoError(t, err)
	assert.Equal(t, &custom_metrics.MetricValue{
		DescribedObject: custom_metrics.ObjectReference{
			Name: "node-1",
			Kind: "nodes",
		},
		Metric: custom_metrics.MetricIdentifier{
			Name: "full_metric_with_node",
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"name": "full_metric_with_node",
				},
			},
		},
		Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() - time.Second.Milliseconds()*2)),
		Value:     resource.MustParse("73"),
	}, oneMetric)

	t.Log("#### 1.2.2: GetMetricByName for pod")

	oneMetric, err = p.GetMetricByName(ctx, types.NamespacedName{
		Namespace: "ns-1",
		Name:      "pod-1",
	}, provider.CustomMetricInfo{
		GroupResource: podGR,
		Namespaced:    false,
		Metric:        "full_metric_with_conflict_time",
	}, labels.SelectorFromSet(labels.Set(map[string]string{
		"name": "full_metric_with_conflict_time",
	})))
	assert.NoError(t, err)
	assert.Equal(t, &custom_metrics.MetricValue{
		DescribedObject: custom_metrics.ObjectReference{
			Namespace: "ns-1",
			Name:      "pod-1",
			Kind:      "pods",
		},
		Metric: custom_metrics.MetricIdentifier{
			Name: "full_metric_with_conflict_time",
			Selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "name",
						Operator: "=",
						Values:   []string{"full_metric_with_conflict_time"},
					},
				},
			},
		},
		Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
		Value:     resource.MustParse("31"),
	}, oneMetric)

	t.Log("#### 1.3.1: GetMetricBySelector empty ns")

	batchMetric, err = p.GetMetricBySelector(ctx, "", labels.Everything(), provider.CustomMetricInfo{GroupResource: nodeGR}, labels.Everything())
	assert.NoError(t, err)
	assert.Equal(t, 1, len(batchMetric.Items))
	assert.ElementsMatch(t, []custom_metrics.MetricValue{
		{
			DescribedObject: custom_metrics.ObjectReference{
				Name: "node-1",
				Kind: "nodes",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_node",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_node",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() - time.Second.Milliseconds()*2)),
			Value:     resource.MustParse("73"),
		},
	}, batchMetric.Items)

	t.Log("#### 1.3.2: GetMetricBySelector byLabel")
	batchMetric, err = p.GetMetricBySelector(ctx, "ns-2",
		labels.SelectorFromSet(labels.Set{"name": "full_metric_with_multiple_data"}),
		provider.CustomMetricInfo{Metric: "full_metric_with_multiple_data", GroupResource: podGR}, labels.Everything())
	assert.NoError(t, err)
	assert.Equal(t, 3, len(batchMetric.Items))
	assert.ElementsMatch(t, []custom_metrics.MetricValue{
		{
			DescribedObject: custom_metrics.ObjectReference{
				Namespace: "ns-2",
				Name:      "pod-2",
				Kind:      "pods",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_multiple_data",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_multiple_data",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
			Value:     resource.MustParse("31"),
		},
		{
			DescribedObject: custom_metrics.ObjectReference{
				Namespace: "ns-2",
				Name:      "pod-2",
				Kind:      "pods",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_multiple_data",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_multiple_data",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() - time.Second.Milliseconds()*3)),
			Value:     resource.MustParse("44"),
		},
		{
			DescribedObject: custom_metrics.ObjectReference{
				Namespace: "ns-2",
				Name:      "pod-2",
				Kind:      "pods",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_multiple_data",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_multiple_data",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() - time.Second.Milliseconds()*2)),
			Value:     resource.MustParse("86"),
		},
	}, batchMetric.Items)

	t.Log("#### 1.3.3: GetMetricBySelector ns-1")

	batchMetric, err = p.GetMetricBySelector(ctx, "ns-1", labels.Everything(), provider.CustomMetricInfo{GroupResource: podGR}, labels.Everything())
	assert.NoError(t, err)
	assert.Equal(t, 1, len(batchMetric.Items))
	assert.ElementsMatch(t, []custom_metrics.MetricValue{
		{
			DescribedObject: custom_metrics.ObjectReference{
				Namespace: "ns-1",
				Name:      "pod-1",
				Kind:      "pods",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_conflict_time",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_conflict_time",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
			Value:     resource.MustParse("31"),
		},
	}, batchMetric.Items)

	t.Log("#### 1.3.4: GetMetricBySelector ns-2")

	batchMetric, err = p.GetMetricBySelector(ctx, "ns-2", labels.Everything(), provider.CustomMetricInfo{GroupResource: podGR}, labels.Everything())
	assert.NoError(t, err)
	assert.Equal(t, 3, len(batchMetric.Items))
	assert.ElementsMatch(t, []custom_metrics.MetricValue{
		{
			DescribedObject: custom_metrics.ObjectReference{
				Namespace: "ns-2",
				Name:      "pod-2",
				Kind:      "pods",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_multiple_data",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_multiple_data",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
			Value:     resource.MustParse("31"),
		},
		{
			DescribedObject: custom_metrics.ObjectReference{
				Namespace: "ns-2",
				Name:      "pod-2",
				Kind:      "pods",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_multiple_data",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_multiple_data",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() - time.Second.Milliseconds()*3)),
			Value:     resource.MustParse("44"),
		},
		{
			DescribedObject: custom_metrics.ObjectReference{
				Namespace: "ns-2",
				Name:      "pod-2",
				Kind:      "pods",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_multiple_data",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_multiple_data",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() - time.Second.Milliseconds()*2)),
			Value:     resource.MustParse("86"),
		},
	}, batchMetric.Items)

	t.Log("#### 1.4: ListAllExternalMetrics")

	externalInfo = p.ListAllExternalMetrics()
	assert.Equal(t, 3, len(externalInfo))
	assert.ElementsMatch(t, []provider.ExternalMetricInfo{
		{
			Metric: "none_namespace_metric",
		},
		{
			Metric: "none_namespace_metric_all_timeout",
		},
		{
			Metric: "none_object_metric",
		},
	}, externalInfo)

	t.Log("#### 1.5.1: GetExternalMetric empty ns")

	batchExternal, err = p.GetExternalMetric(ctx, "", labels.Everything(), provider.ExternalMetricInfo{})
	assert.NoError(t, err)
	assert.Equal(t, 3, len(batchExternal.Items))
	assert.ElementsMatch(t, []external_metrics.ExternalMetricValue{
		{
			MetricName: "none_namespace_metric",
			MetricLabels: map[string]string{
				"name": "none_namespace_metric",
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
			Value:     resource.MustParse("1"),
		},
		{
			MetricName: "none_namespace_metric",
			MetricLabels: map[string]string{
				"name": "none_namespace_metric",
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() + time.Second.Milliseconds()*5)),
			Value:     resource.MustParse("2"),
		},
		{
			MetricName: "none_namespace_metric_all_timeout",
			MetricLabels: map[string]string{
				"name": "none_namespace_metric_all_timeout",
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds() + time.Second.Milliseconds()*5)),
			Value:     resource.MustParse("2"),
		},
	}, batchExternal.Items)

	t.Log("#### 1.5.2: GetExternalMetric ns-1")

	batchExternal, err = p.GetExternalMetric(ctx, "ns-1", labels.Everything(), provider.ExternalMetricInfo{})
	assert.NoError(t, err)
	assert.Equal(t, 2, len(batchExternal.Items))
	assert.ElementsMatch(t, []external_metrics.ExternalMetricValue{
		{
			MetricName: "none_object_metric",
			MetricLabels: map[string]string{
				"name": "none_object_metric",
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
			Value:     resource.MustParse("19"),
		},
		{
			MetricName: "none_object_metric",
			MetricLabels: map[string]string{
				"name": "none_object_metric",
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli() - genericConf.OutOfDataPeriod.Milliseconds())),
			Value:     resource.MustParse("23"),
		},
	}, batchExternal.Items)

	// sleep a while to trigger gc
	time.Sleep(time.Second * 25)

	t.Log("#### 2.1: ListAllMetrics")

	metricInfo = p.ListAllMetrics()
	assert.Equal(t, 3, len(metricInfo))
	assert.ElementsMatch(t, []provider.CustomMetricInfo{
		{
			GroupResource: podGR,
			Namespaced:    true,
			Metric:        "full_metric_with_conflict_time",
		},
		{
			GroupResource: podGR,
			Namespaced:    true,
			Metric:        "full_metric_with_multiple_data",
		},
		{
			GroupResource: nodeGR,
			Namespaced:    false,
			Metric:        "full_metric_with_node",
		},
	}, metricInfo)

	t.Log("#### 2.3.1: GetMetricBySelector empty ns")

	batchMetric, err = p.GetMetricBySelector(ctx, "", labels.Everything(), provider.CustomMetricInfo{GroupResource: podGR}, labels.Everything())
	assert.NoError(t, err)
	assert.Equal(t, 0, len(batchMetric.Items))

	t.Log("#### 2.3.2: GetMetricBySelector ns-1")

	batchMetric, err = p.GetMetricBySelector(ctx, "ns-1", labels.Everything(), provider.CustomMetricInfo{GroupResource: podGR}, labels.Everything())
	assert.NoError(t, err)
	assert.Equal(t, 1, len(batchMetric.Items))
	assert.ElementsMatch(t, []custom_metrics.MetricValue{
		{
			DescribedObject: custom_metrics.ObjectReference{
				Namespace: "ns-1",
				Name:      "pod-1",
				Kind:      "pods",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_conflict_time",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_conflict_time",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
			Value:     resource.MustParse("31"),
		},
	}, batchMetric.Items)

	t.Log("#### 2.3.3: GetMetricBySelector ns-2")

	batchMetric, err = p.GetMetricBySelector(ctx, "ns-2", labels.Everything(), provider.CustomMetricInfo{GroupResource: podGR}, labels.Everything())
	assert.NoError(t, err)
	assert.Equal(t, 1, len(batchMetric.Items))
	assert.ElementsMatch(t, []custom_metrics.MetricValue{
		{
			DescribedObject: custom_metrics.ObjectReference{
				Namespace: "ns-2",
				Name:      "pod-2",
				Kind:      "pods",
			},
			Metric: custom_metrics.MetricIdentifier{
				Name: "full_metric_with_multiple_data",
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"name": "full_metric_with_multiple_data",
					},
				},
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
			Value:     resource.MustParse("31"),
		},
	}, batchMetric.Items)

	t.Log("#### 2.4: ListAllExternalMetrics")

	externalInfo = p.ListAllExternalMetrics()
	assert.Equal(t, 3, len(externalInfo))
	assert.ElementsMatch(t, []provider.ExternalMetricInfo{
		{
			Metric: "none_namespace_metric",
		},
		{
			Metric: "none_object_metric",
		},
		{
			Metric: "none_namespace_metric_all_timeout",
		},
	}, externalInfo)

	t.Log("#### 2.5.1: GetExternalMetric empty ns")

	batchExternal, err = p.GetExternalMetric(ctx, "", labels.Everything(), provider.ExternalMetricInfo{})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(batchExternal.Items))
	assert.ElementsMatch(t, []external_metrics.ExternalMetricValue{
		{
			MetricName: "none_namespace_metric",
			MetricLabels: map[string]string{
				"name": "none_namespace_metric",
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
			Value:     resource.MustParse("1"),
		},
	}, batchExternal.Items)

	t.Log("#### 2.5.2: GetExternalMetric ns-1")

	batchExternal, err = p.GetExternalMetric(ctx, "ns-1", labels.Everything(), provider.ExternalMetricInfo{})
	assert.NoError(t, err)
	assert.Equal(t, 1, len(batchExternal.Items))
	assert.ElementsMatch(t, []external_metrics.ExternalMetricValue{
		{
			MetricName: "none_object_metric",
			MetricLabels: map[string]string{
				"name": "none_object_metric",
			},
			Timestamp: metav1.NewTime(time.UnixMilli(now.UnixMilli())),
			Value:     resource.MustParse("19"),
		},
	}, batchExternal.Items)

	_ = s.Stop()
}
