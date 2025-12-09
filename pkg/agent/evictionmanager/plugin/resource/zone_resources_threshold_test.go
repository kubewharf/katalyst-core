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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
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

func TestZoneResourcesPlugin_ThresholdMet_GPU(t *testing.T) {
	t.Parallel()

	pods := []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "default", UID: "u1"}}}
	cnrObj := &nodev1alpha1.CustomNodeResource{
		ObjectMeta: metav1.ObjectMeta{Name: "n"},
		Status: nodev1alpha1.CustomNodeResourceStatus{
			TopologyZone: []*nodev1alpha1.TopologyZone{{
				Name: "0",
				Type: nodev1alpha1.TopologyTypeGPU,
				Resources: nodev1alpha1.Resources{Allocatable: &corev1.ResourceList{
					corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
				}},
				Allocations: []*nodev1alpha1.Allocation{{
					Consumer: "default/p1/u1",
					Requests: &corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1")},
				}},
			}},
		},
	}
	ctx, err := katalyst_base.GenerateFakeGenericContext(nil, []runtime.Object{cnrObj}, nil)
	if err != nil {
		t.Fatalf("context error: %v", err)
	}
	// use configuration to build fetchers similar to existing tests
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
	p := NewZoneResourcesPlugin("test", nodev1alpha1.TopologyTypeGPU, ms, metrics.DummyMetrics{}, nil,
		func(corev1.ResourceName) *float64 { r := 0.5; return &r }, func() int64 { return 0 }, func() int64 { return 0 },
		nil, func(*corev1.Pod) (bool, error) { return true, nil })
	met, err := p.ThresholdMet(context.TODO(), nil)
	if err != nil {
		t.Fatalf("threshold err: %v", err)
	}
	if met == nil || met.MetType != 2 { // HARD_MET
		t.Fatalf("unexpected threshold resp: %+v", met)
	}
}

func TestZoneResourcesPlugin_ThresholdMet_SkipZero(t *testing.T) {
	t.Parallel()

	pods := []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "p1", Namespace: "default", UID: "u1"}}}
	cnrObj := &nodev1alpha1.CustomNodeResource{
		ObjectMeta: metav1.ObjectMeta{Name: "n"},
		Status: nodev1alpha1.CustomNodeResourceStatus{
			TopologyZone: []*nodev1alpha1.TopologyZone{{
				Name: "0",
				Type: nodev1alpha1.TopologyTypeGPU,
				Resources: nodev1alpha1.Resources{Allocatable: &corev1.ResourceList{
					corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("0"),
				}},
				Allocations: []*nodev1alpha1.Allocation{{
					Consumer: "default/p1/u1",
					Requests: &corev1.ResourceList{corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1")},
				}},
			}},
		},
	}
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
	skip := sets.NewString("nvidia.com/gpu")
	p := NewZoneResourcesPlugin("test", nodev1alpha1.TopologyTypeGPU, ms, metrics.DummyMetrics{}, nil,
		func(corev1.ResourceName) *float64 { r := 0.5; return &r }, func() int64 { return 0 }, func() int64 { return 0 },
		skip, func(*corev1.Pod) (bool, error) { return true, nil })
	met, err := p.ThresholdMet(context.TODO(), nil)
	if err != nil {
		t.Fatalf("threshold err: %v", err)
	}
	if met == nil || met.MetType != pluginapi.ThresholdMetType_NOT_MET {
		t.Fatalf("expected NOT_MET, got %+v", met)
	}
}
