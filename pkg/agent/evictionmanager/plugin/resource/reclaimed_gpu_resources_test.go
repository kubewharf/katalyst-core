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
	"k8s.io/client-go/tools/events"

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

func TestNewReclaimedGPUResourcesEvictionPlugin_Behavior(t *testing.T) {
	t.Parallel()

	testNodeName := "gpu-node"
	testConf, err := options.NewOptions().Config()
	if err != nil {
		t.Fatalf("config error: %v", err)
	}
	testConf.NodeName = testNodeName
	// configure threshold for GPU resource
	if testConf.GetDynamicConfiguration().EvictionThreshold == nil {
		testConf.GetDynamicConfiguration().EvictionThreshold = map[corev1.ResourceName]float64{}
	}
	testConf.GetDynamicConfiguration().EvictionThreshold[corev1.ResourceName("nvidia.com/gpu")] = 0.5

	// pods
	podA := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-a",
			Namespace: "default",
			UID:       "uid-a",
			Annotations: map[string]string{
				"katalyst.kubewharf.io/qos_level": "reclaimed_cores",
			},
		},
	}
	podB := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod-b",
			Namespace: "default",
			UID:       "uid-b",
			Annotations: map[string]string{
				"katalyst.kubewharf.io/qos_level": "reclaimed_cores",
			},
		},
	}
	pods := []*corev1.Pod{podA, podB}

	// CNR with GPU topology and allocations
	cnrObj := &nodev1alpha1.CustomNodeResource{
		ObjectMeta: metav1.ObjectMeta{Name: testNodeName},
		Status: nodev1alpha1.CustomNodeResourceStatus{
			TopologyZone: []*nodev1alpha1.TopologyZone{
				{
					Name: "0",
					Type: nodev1alpha1.TopologyTypeGPU,
					Resources: nodev1alpha1.Resources{
						Allocatable: &corev1.ResourceList{
							corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("2"),
						},
					},
					Allocations: []*nodev1alpha1.Allocation{
						{
							Consumer: "default/pod-a/uid-a",
							Requests: &corev1.ResourceList{
								corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
							},
						},
						{
							Consumer: "default/pod-b/uid-b",
							Requests: &corev1.ResourceList{
								corev1.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
							},
						},
					},
				},
			},
		},
	}

	ctx, err := katalyst_base.GenerateFakeGenericContext(nil, []runtime.Object{cnrObj}, nil)
	if err != nil {
		t.Fatalf("context error: %v", err)
	}

	ms := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher: &pod.PodFetcherStub{PodList: pods},
			NodeFetcher: node.NewRemoteNodeFetcher(testConf.BaseConfiguration, testConf.NodeConfiguration,
				ctx.Client.KubeClient.CoreV1().Nodes()),
			CNRFetcher: cnr.NewCachedCNRFetcher(testConf.BaseConfiguration, testConf.CNRConfiguration,
				ctx.Client.InternalClient.NodeV1alpha1().CustomNodeResources()),
		},
	}

	plugin := NewReclaimedGPUResourcesEvictionPlugin(ctx.Client, &events.FakeRecorder{}, ms, metrics.DummyMetrics{}, testConf)
	if plugin == nil {
		t.Fatalf("plugin nil")
	}

	// ThresholdMet
	met, err := plugin.ThresholdMet(context.TODO(), &pluginapi.GetThresholdMetRequest{})
	if err != nil {
		t.Fatalf("threshold error: %v", err)
	}
	if met == nil {
		t.Fatalf("threshold nil")
	}

	// GetTopEvictionPods
	podsResp, err := plugin.GetTopEvictionPods(context.TODO(), &pluginapi.GetTopEvictionPodsRequest{
		ActivePods:    pods,
		TopN:          1,
		EvictionScope: met.EvictionScope,
	})
	if err != nil {
		t.Fatalf("top eviction error: %v", err)
	}
	if len(podsResp.GetTargetPods()) == 0 {
		t.Fatalf("no target pods")
	}

	// GetEvictPods (currently returns empty but should be non-nil)
	evictResp, err := plugin.GetEvictPods(context.TODO(), &pluginapi.GetEvictPodsRequest{ActivePods: pods})
	if err != nil {
		t.Fatalf("evict pods error: %v", err)
	}
	if evictResp == nil {
		t.Fatalf("evict resp nil")
	}
}
