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

package npd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	v12 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	metrics_plugin "github.com/kubewharf/katalyst-core/pkg/controller/npd/metrics-plugin"
)

func TestMetricsUpdater(t *testing.T) {
	t.Parallel()

	ctx := context.TODO()
	npdConfig := &controller.NPDConfig{
		NPDMetricsPlugins:     []string{"plugin1", "plugin2"},
		SyncWorkers:           1,
		EnableScopeDuplicated: false,
	}
	genericConfig := &generic.GenericConfiguration{}

	nodes := []*v1.Node{
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "node1",
			},
		},
		{
			ObjectMeta: v12.ObjectMeta{
				Name: "node2",
			},
		},
	}
	npd := &v1alpha1.NodeProfileDescriptor{
		ObjectMeta: v12.ObjectMeta{
			Name: "node1",
		},
		Spec: v1alpha1.NodeProfileDescriptorSpec{},
		Status: v1alpha1.NodeProfileDescriptorStatus{
			NodeMetrics: []v1alpha1.ScopedNodeMetrics{},
			PodMetrics:  []v1alpha1.ScopedPodMetrics{},
		},
	}
	controlCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{nodes[0], nodes[1]},
		[]runtime.Object{npd}, []runtime.Object{})
	assert.NoError(t, err)

	// register plugins
	metrics_plugin.RegisterPluginInitializer("plugin1", func(ctx context.Context, conf *controller.NPDConfig, extraConf interface{}, controlCtx *katalystbase.GenericContext, updater metrics_plugin.MetricsUpdater) (metrics_plugin.MetricsPlugin, error) {
		return metrics_plugin.DummyMetricsPlugin{
			NodeMetricsScopes: []string{
				"scope1", "scope2",
			},
			PodMetricsScopes: []string{
				"scope1", "scope2",
			},
		}, nil
	})
	metrics_plugin.RegisterPluginInitializer("plugin2", func(ctx context.Context, conf *controller.NPDConfig, extraConf interface{}, controlCtx *katalystbase.GenericContext, updater metrics_plugin.MetricsUpdater) (metrics_plugin.MetricsPlugin, error) {
		return metrics_plugin.DummyMetricsPlugin{
			NodeMetricsScopes: []string{
				"scope3", "scope4",
			},
			PodMetricsScopes: []string{
				"scope3", "scope4",
			},
		}, nil
	})

	npdController, err := NewNPDController(ctx, controlCtx, genericConfig, nil, npdConfig, nil)
	assert.NoError(t, err)

	controlCtx.StartInformer(ctx)
	go npdController.Run()
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	synced := cache.WaitForCacheSync(timeout.Done(), npdController.syncedFunc...)
	assert.True(t, synced)

	manager := npdController.metricsManager.(*metrics_plugin.MetricsManager)
	manager.UpdateNodeMetrics("node1", []v1alpha1.ScopedNodeMetrics{
		{
			Scope: "scope1",
			Metrics: []v1alpha1.MetricValue{
				{
					MetricName: "cpu",
					Value:      resource.MustParse("1"),
				},
			},
		},
	})
	manager.UpdateNodeMetrics("node1", []v1alpha1.ScopedNodeMetrics{
		{
			Scope: "scope2",
			Metrics: []v1alpha1.MetricValue{
				{
					MetricName: "cpu",
					Value:      resource.MustParse("2"),
				},
			},
		},
	})
	manager.UpdatePodMetrics("node1", []v1alpha1.ScopedPodMetrics{
		{
			Scope: "scope1",
			PodMetrics: []v1alpha1.PodMetric{
				{
					Namespace: "default",
					Name:      "pod1",
					Metrics: []v1alpha1.MetricValue{
						{
							MetricName: "cpu",
							Value:      resource.MustParse("1"),
						},
					},
				},
			},
		},
	})
	manager.UpdatePodMetrics("node2", []v1alpha1.ScopedPodMetrics{
		{
			Scope: "scope4",
			PodMetrics: []v1alpha1.PodMetric{
				{
					Namespace: "default",
					Name:      "pod2",
					Metrics: []v1alpha1.MetricValue{
						{
							MetricName: "cpu",
							Value:      resource.MustParse("1"),
						},
					},
				},
			},
		},
	})
	manager.UpdateNodeMetrics("node2", []v1alpha1.ScopedNodeMetrics{
		{
			Scope: "scope3",
			Metrics: []v1alpha1.MetricValue{
				{
					MetricName: "memory",
					Value:      resource.MustParse("8Gi"),
				},
			},
		},
	})
	manager.UpdateNodeMetrics("node1", []v1alpha1.ScopedNodeMetrics{
		{
			Scope: "unsupported",
			Metrics: []v1alpha1.MetricValue{
				{
					MetricName: "memory",
					Value:      resource.MustParse("8Gi"),
				},
			},
		},
	})
	manager.UpdatePodMetrics("node2", []v1alpha1.ScopedPodMetrics{
		{
			Scope: "unsupported",
			PodMetrics: []v1alpha1.PodMetric{
				{
					Namespace: "default",
					Name:      "pod2",
					Metrics: []v1alpha1.MetricValue{
						{
							MetricName: "cpu",
							Value:      resource.MustParse("1"),
						},
					},
				},
			},
		},
	})

	expectedNPD1 := v1alpha1.NodeProfileDescriptorStatus{
		NodeMetrics: []v1alpha1.ScopedNodeMetrics{
			{
				Scope: "scope1",
				Metrics: []v1alpha1.MetricValue{
					{
						MetricName: "cpu",
						Value:      resource.MustParse("1"),
					},
				},
			},
			{
				Scope: "scope2",
				Metrics: []v1alpha1.MetricValue{
					{
						MetricName: "cpu",
						Value:      resource.MustParse("2"),
					},
				},
			},
		},
		PodMetrics: []v1alpha1.ScopedPodMetrics{
			{
				Scope: "scope1",
				PodMetrics: []v1alpha1.PodMetric{
					{
						Namespace: "default",
						Name:      "pod1",
						Metrics: []v1alpha1.MetricValue{
							{
								MetricName: "cpu",
								Value:      resource.MustParse("1"),
							},
						},
					},
				},
			},
		},
	}
	expectedNPD2 := v1alpha1.NodeProfileDescriptorStatus{
		NodeMetrics: []v1alpha1.ScopedNodeMetrics{
			{
				Scope: "scope3",
				Metrics: []v1alpha1.MetricValue{
					{
						MetricName: "memory",
						Value:      resource.MustParse("8Gi"),
					},
				},
			},
		},
		PodMetrics: []v1alpha1.ScopedPodMetrics{
			{
				Scope: "scope4",
				PodMetrics: []v1alpha1.PodMetric{
					{
						Namespace: "default",
						Name:      "pod2",
						Metrics: []v1alpha1.MetricValue{
							{
								MetricName: "cpu",
								Value:      resource.MustParse("1"),
							},
						},
					},
				},
			},
		},
	}

	time.Sleep(time.Second)
	node1NPD, err := controlCtx.Client.InternalClient.NodeV1alpha1().NodeProfileDescriptors().Get(ctx, "node1", v12.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, expectedNPD1, node1NPD.Status)

	node2NPD, err := controlCtx.Client.InternalClient.NodeV1alpha1().NodeProfileDescriptors().Get(ctx, "node2", v12.GetOptions{})
	assert.NoError(t, err)
	assert.Equal(t, expectedNPD2, node2NPD.Status)

	err = controlCtx.Client.KubeClient.CoreV1().Nodes().Delete(ctx, "node2", v12.DeleteOptions{})
	assert.NoError(t, err)
	time.Sleep(time.Second)
	_, err = controlCtx.Client.InternalClient.NodeV1alpha1().NodeProfileDescriptors().Get(ctx, "node2", v12.GetOptions{})
	assert.Error(t, err)
	assert.True(t, errors.IsNotFound(err))
}
