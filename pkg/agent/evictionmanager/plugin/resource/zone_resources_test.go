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

package resource

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// minimal metaServer stub (unused in tests below)
func makeMinimalMetaServer() *metaserver.MetaServer { return &metaserver.MetaServer{} }

func TestZoneResourcesPlugin_BasicAPIs(t *testing.T) {
	t.Parallel()

	p := NewZoneResourcesPlugin(
		"test-plugin",
		v1alpha1.TopologyTypeGPU,
		makeMinimalMetaServer(),
		metrics.DummyMetrics{},
		nil,
		func(corev1.ResourceName) *float64 { return nil },
		func() int64 { return 0 },
		func() int64 { return 0 },
		nil,
		func(pod *corev1.Pod) (bool, error) { return true, nil },
	)

	if p.Name() != "test-plugin" {
		t.Fatalf("unexpected name: %s", p.Name())
	}

	p.Start() // should not panic

	// GetEvictPods nil request
	if _, err := p.GetEvictPods(context.TODO(), nil); err == nil {
		t.Fatalf("expected error on nil request")
	}

	// GetEvictPods non-nil request returns non-nil response
	resp, err := p.GetEvictPods(context.TODO(), &pluginapi.GetEvictPodsRequest{})
	if err != nil || resp == nil {
		t.Fatalf("unexpected GetEvictPods result: resp=%v err=%v", resp, err)
	}
}

func TestZoneResourcesPlugin_NameNil(t *testing.T) {
	t.Parallel()
	var p *ZoneResourcesPlugin
	if p.Name() != "" {
		t.Fatalf("expected empty name for nil receiver")
	}
}

func TestZoneResourcesPlugin_GetTopEvictionPods_EmptyActive(t *testing.T) {
	t.Parallel()
	p := NewZoneResourcesPlugin(
		"test-plugin",
		v1alpha1.TopologyTypeGPU,
		makeMinimalMetaServer(),
		metrics.DummyMetrics{},
		nil,
		func(corev1.ResourceName) *float64 { return nil },
		func() int64 { return 1 },
		func() int64 { return 0 },
		nil,
		func(pod *corev1.Pod) (bool, error) { return true, nil },
	)
	resp, err := p.GetTopEvictionPods(context.TODO(), &pluginapi.GetTopEvictionPodsRequest{ActivePods: nil})
	if err != nil || resp == nil {
		t.Fatalf("unexpected resp: %v err: %v", resp, err)
	}
}

func TestZoneResourcesPlugin_ThresholdMet_NoFilteredPods(t *testing.T) {
	t.Parallel()
	pods := []*corev1.Pod{{}}
	cnrObj := &v1alpha1.CustomNodeResource{ObjectMeta: v1.ObjectMeta{Name: "n"}}
	ctx, err := katalyst_base.GenerateFakeGenericContext(nil, []runtime.Object{cnrObj}, nil)
	if err != nil {
		t.Fatalf("context error: %v", err)
	}
	conf, confErr := options.NewOptions().Config()
	if confErr != nil {
		t.Fatalf("config error: %v", confErr)
	}
	conf.NodeName = "n"
	ms := &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{
		PodFetcher: &pod.PodFetcherStub{PodList: pods},
		NodeFetcher: node.NewRemoteNodeFetcher(conf.BaseConfiguration, conf.NodeConfiguration,
			ctx.Client.KubeClient.CoreV1().Nodes()),
		CNRFetcher: cnr.NewCachedCNRFetcher(conf.BaseConfiguration, conf.CNRConfiguration,
			ctx.Client.InternalClient.NodeV1alpha1().CustomNodeResources()),
	}}
	p := NewZoneResourcesPlugin(
		"test-plugin",
		v1alpha1.TopologyTypeGPU,
		ms,
		metrics.DummyMetrics{},
		nil,
		func(corev1.ResourceName) *float64 { r := 0.5; return &r },
		func() int64 { return 0 },
		func() int64 { return 0 },
		nil,
		func(pod *corev1.Pod) (bool, error) { return false, nil },
	)
	met, err := p.ThresholdMet(context.TODO(), &pluginapi.GetThresholdMetRequest{})
	if err != nil || met == nil {
		t.Fatalf("unexpected resp: %v err: %v", met, err)
	}
}

// invalid scope parsing path relies on MetaServer.GetCNR; covered indirectly by GPU/NUMA plugin tests.

func TestGenericPodZoneRequestResourcesGetter(t *testing.T) {
	t.Parallel()
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{UID: "uid-x"}}
	alloc := map[string]ZoneAllocation{
		"uid-x": {
			"0": corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
		},
	}
	got := GenericPodZoneRequestResourcesGetter(p, "0", alloc)
	if got == nil {
		t.Fatalf("expected resource list, got nil")
	}
}
