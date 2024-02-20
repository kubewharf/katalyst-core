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

package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/apis/config"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	v1alpha1fake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	v1alpha1client "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/typed/node/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	metaconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	cnrmeta "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/kubeletconfig"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func constructNodeInterface(name string) corev1.NodeInterface {
	objects := []runtime.Object{
		&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
	return fake.NewSimpleClientset(objects...).CoreV1().Nodes()
}

func constructCNRInterface(name string) v1alpha1client.CustomNodeResourceInterface {
	objects := []runtime.Object{
		&v1alpha1.CustomNodeResource{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
	return v1alpha1fake.NewSimpleClientset(objects...).NodeV1alpha1().CustomNodeResources()
}

func constructPodFetcher(names []string) pod.PodFetcher {
	var pods []*v1.Pod
	for _, name := range names {
		pods = append(pods, &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
				UID:  types.UID(name + "-uid"),
			},
		})
	}

	return &pod.PodFetcherStub{PodList: pods}
}

type ObjectFetcherTest struct {
	obj *unstructured.Unstructured
}

func (o *ObjectFetcherTest) GetUnstructured(_ context.Context, _, _ string) (*unstructured.Unstructured, error) {
	return o.obj, nil
}

func TestFetcher(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	conf, _ := options.NewOptions().Config()
	conf.CheckpointManagerDir = "/tmp/TestFetcher"
	bCtx, _ := katalyst_base.GenerateFakeGenericContext(nil, nil, nil)

	agent, err := NewMetaAgent(conf, bCtx.Client, metrics.DummyMetrics{})
	assert.NoError(t, err)

	// before start, we can set component implementations for metaServer
	agent.SetPodFetcher(constructPodFetcher([]string{"test-pod-1", "test-pod-2"}))
	agent.SetNodeFetcher(node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: "test-node-1"}, &metaconfig.NodeConfiguration{}, constructNodeInterface("test-node-1")))
	agent.SetCNRFetcher(cnrmeta.NewCachedCNRFetcher(&global.BaseConfiguration{NodeName: "test-cnr-1"}, conf.CNRConfiguration, constructCNRInterface("test-cnr-1")))
	agent.SetMetricFetcher(metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	agent.SetKubeletConfigFetcher(kubeletconfig.NewKubeletConfigFetcher(conf.BaseConfiguration, metrics.DummyMetrics{}))

	podObjList, err := agent.GetPodList(ctx, func(*v1.Pod) bool { return true })
	assert.NoError(t, err)
	assert.Equal(t, 2, len(podObjList))

	pod1, err := agent.GetPod(ctx, "test-pod-1-uid")
	assert.NoError(t, err)
	assert.Equal(t, "test-pod-1", pod1.Name)

	nodeObj, err := agent.GetNode(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-node-1", nodeObj.Name)

	cnrObj, err := agent.GetCNR(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-cnr-1", cnrObj.Name)

	_, err1 := agent.GetKubeletConfig(ctx)
	assert.Error(t, err1)

	gvr := metav1.GroupVersionResource{Group: "a", Version: "b", Resource: "c"}
	_, gErr := agent.GetUnstructured(ctx, gvr, "", "aaa")
	assert.NotNil(t, gErr)

	// before start, we can set component implementations for metaServer
	agent.SetPodFetcher(constructPodFetcher([]string{"test-pod-3", "test-pod-4", "test-pod-5"}))
	agent.SetNodeFetcher(node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: "test-node-2"}, &metaconfig.NodeConfiguration{}, constructNodeInterface("test-node-2")))
	agent.SetCNRFetcher(cnrmeta.NewCachedCNRFetcher(&global.BaseConfiguration{NodeName: "test-cnr-2"}, conf.CNRConfiguration, constructCNRInterface("test-cnr-2")))

	conf.KubeletSecurePortEnabled = true
	agent.SetKubeletConfigFetcher(kubeletconfig.NewKubeletConfigFetcher(conf.BaseConfiguration, metrics.DummyMetrics{}))

	_ = agent.CNRFetcher.RegisterNotifier("test-cnr-2", cnrmeta.CNRNotifierStub{})
	assert.NoError(t, err)
	defer func() {
		err := agent.CNRFetcher.UnregisterNotifier("test-cnr-2")
		assert.NoError(t, err)
	}()

	podObjList, err = agent.GetPodList(ctx, func(*v1.Pod) bool { return true })
	assert.NoError(t, err)
	assert.Equal(t, 3, len(podObjList))

	nodeObj, err = agent.GetNode(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-node-2", nodeObj.Name)

	cnrObj, err = agent.GetCNR(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-cnr-2", cnrObj.Name)

	_, err2 := agent.GetKubeletConfig(ctx)
	assert.Error(t, err2)

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"lll": "bbb",
		},
	}
	s := &ObjectFetcherTest{obj: obj}

	fakeKubeletConfig := kubeletconfigv1beta1.KubeletConfiguration{
		TopologyManagerPolicy: config.SingleNumaNodeTopologyManagerPolicy,
		TopologyManagerScope:  config.ContainerTopologyManagerScope,
	}
	agent.SetKubeletConfigFetcher(kubeletconfig.NewFakeKubeletConfigFetcher(fakeKubeletConfig))

	// after start, we can't set component implementations for metaServer
	go agent.Run(ctx)
	time.Sleep(time.Second)

	agent.SetPodFetcher(constructPodFetcher([]string{"test-pod-6"}))
	agent.SetNodeFetcher(node.NewRemoteNodeFetcher(&global.BaseConfiguration{NodeName: "test-node-3"}, &metaconfig.NodeConfiguration{}, constructNodeInterface("test-node-3")))
	agent.SetCNRFetcher(cnrmeta.NewCachedCNRFetcher(&global.BaseConfiguration{NodeName: "test-cnr-3"}, conf.CNRConfiguration, constructCNRInterface("test-cnr-3")))
	agent.SetObjectFetcher(gvr, s)

	podObjList, err = agent.GetPodList(ctx, func(*v1.Pod) bool { return true })
	assert.NoError(t, err)
	assert.Equal(t, 3, len(podObjList))

	nodeObj, err = agent.GetNode(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-node-2", nodeObj.Name)

	cnrObj, err = agent.GetCNR(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "test-cnr-2", cnrObj.Name)

	o, err := agent.GetUnstructured(ctx, gvr, "", "aaa")
	assert.NoError(t, err)
	assert.Equal(t, "bbb", o.Object["lll"])

	kubeletConfig, err3 := agent.GetKubeletConfig(ctx)
	assert.NoError(t, err3)
	assert.Equal(t, config.SingleNumaNodeTopologyManagerPolicy, kubeletConfig.TopologyManagerPolicy)
	assert.Equal(t, config.ContainerTopologyManagerScope, kubeletConfig.TopologyManagerScope)

	cancel()
}
