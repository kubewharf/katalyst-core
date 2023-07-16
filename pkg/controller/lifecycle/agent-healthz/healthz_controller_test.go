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

package agent_healthz

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/options"
	"github.com/kubewharf/katalyst-core/pkg/client"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
)

func generateTestKubeClientSet(objects []runtime.Object, internalObjects []runtime.Object) *client.GenericClientSet {
	scheme := runtime.NewScheme()
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	return &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(objects...),
		InternalClient: internalfake.NewSimpleClientset(internalObjects...),
	}
}

func generateTestConfiguration(t *testing.T) *pkgconfig.Configuration {
	seletor, _ := labels.Parse("app=katalyst-agent")

	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	testConfiguration.ControllersConfiguration.LifeCycleConfig.HandlePeriod = time.Second
	testConfiguration.ControllersConfiguration.LifeCycleConfig.UnhealthyPeriods = 5 * time.Second
	testConfiguration.ControllersConfiguration.LifeCycleConfig.AgentSelector = map[string]labels.Selector{
		"katalyst-agent": seletor,
	}

	return testConfiguration
}

func NewFakeHealthzController(t *testing.T) (*HealthzController, error) {
	stopCh := make(chan struct{})
	defer close(stopCh)

	label := make(map[string]string)
	label["app"] = "katalyst-agent"

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "pod1",
			Labels: label,
		},
		Spec: corev1.PodSpec{
			NodeName: "node1",
			Containers: []corev1.Container{
				{
					Name: "katalyst-agent",
				},
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true},
			},
		},
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
			UID:  "1",
		},
		Spec: corev1.NodeSpec{},
	}

	cnr := &apis.CustomNodeResource{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node1",
		},
		Spec: apis.CustomNodeResourceSpec{
			Taints: []*apis.Taint{},
		},
		Status: apis.CustomNodeResourceStatus{},
	}

	clientSet := generateTestKubeClientSet([]runtime.Object{pod, node}, []runtime.Object{cnr})
	kubeInformerFactory := informers.NewSharedInformerFactoryWithOptions(clientSet.KubeClient, time.Hour*24)
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()
	podInformer := kubeInformerFactory.Core().V1().Pods()

	internalInformerFactory := externalversions.NewSharedInformerFactoryWithOptions(clientSet.InternalClient, time.Hour*24)
	cnrInformer := internalInformerFactory.Node().V1alpha1().CustomNodeResources()

	conf := generateTestConfiguration(t)
	ec, err := NewHealthzController(context.Background(), conf.GenericConfiguration, conf.GenericControllerConfiguration,
		conf.ControllersConfiguration.LifeCycleConfig, clientSet, nodeInformer, podInformer, cnrInformer, nil)
	if err != nil {
		t.Errorf("new eviction controller error")
	}

	kubeInformerFactory.Start(stopCh)
	internalInformerFactory.Start(stopCh)

	if !cache.WaitForCacheSync(ec.ctx.Done(), ec.nodeListerSynced, ec.cnrListerSynced, ec.podListerSynced) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for evcition controller"))
	}
	return ec, nil
}

func TestHealthzController(t *testing.T) {
	t.Parallel()

	ec, err := NewFakeHealthzController(t)
	if err != nil {
		klog.Errorf("get new fake cnr lifecycle err %v", err)
	}

	go ec.Run()

	healthy := ec.healthzHelper.CheckAllAgentReady("node1")
	assert.Equal(t, true, healthy)
}
