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

package lifecycle

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
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
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	testConfiguration.ControllersConfiguration.LifeCycleConfig.CNRMonitorGracePeriod = 1 * time.Second
	testConfiguration.ControllersConfiguration.LifeCycleConfig.CNRMonitorTaintPeriod = 5 * time.Second
	testConfiguration.ControllersConfiguration.LifeCycleConfig.CNRMonitorPeriod = 1 * time.Second

	return testConfiguration
}

func NewFakeEvictionController(t *testing.T) (*EvictionController, error) {
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
		Status: apis.CustomNodeResourceStatus{
			Conditions: []apis.CNRCondition{
				{Status: corev1.ConditionTrue, Type: apis.CNRAgentNotFound, LastHeartbeatTime: metav1.Now()},
				{Status: corev1.ConditionFalse, Type: apis.CNRAgentReady, LastHeartbeatTime: metav1.Now()},
			},
		},
	}

	clientSet := generateTestKubeClientSet([]runtime.Object{pod, node}, []runtime.Object{cnr})
	kubeInformerFactory := informers.NewSharedInformerFactoryWithOptions(clientSet.KubeClient, time.Hour*24)
	nodeInformer := kubeInformerFactory.Core().V1().Nodes()
	podInformer := kubeInformerFactory.Core().V1().Pods()

	internalInformerFactory := externalversions.NewSharedInformerFactoryWithOptions(clientSet.InternalClient, time.Hour*24)
	cnrInformer := internalInformerFactory.Node().V1alpha1().CustomNodeResources()

	conf := generateTestConfiguration(t)
	ec, err := NewEvictionController(context.Background(), conf.GenericConfiguration, conf.GenericControllerConfiguration,
		conf.ControllersConfiguration.LifeCycleConfig, clientSet, nodeInformer, podInformer, cnrInformer, nil)
	if err != nil {
		t.Errorf("new eviction controller error")
	}

	kubeInformerFactory.Start(stopCh)
	internalInformerFactory.Start(stopCh)

	if !cache.WaitForCacheSync(ec.ctx.Done(), ec.nodeListerSynced, ec.cnrListerSynced, ec.podListerSynced) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s controller", cnrLifecycleControllerName))
	}
	return ec, nil
}

func TestTaintCNR(t *testing.T) {
	ec, err := NewFakeEvictionController(t)
	if err != nil {
		klog.Errorf("get new fake cnr lifecycle err %v", err)
	}

	cnr1, err := ec.cnrLister.Get("node1")
	if err != nil {
		klog.Errorf("Get cnr1 %v error:%v", cnr1.Name, err)
	}

	newCNR, _, _ := util.AddOrUpdateCNRTaint(cnr1, &util.NoScheduleForReclaimedTasksTaint)

	if newCNR.Spec.Taints[0].Key != corev1.TaintNodeUnschedulable || newCNR.Spec.Taints[0].Effect != apis.TaintEffectNoScheduleForReclaimedTasks {
		t.Errorf("tanit cnr error")
	}

	_, ok, _ := util.RemoveCNRTaint(newCNR, &util.NoScheduleForReclaimedTasksTaint)
	if !ok {
		t.Errorf("remove tanit cnr error")
	}
}

func TestTaintAndEvict(t *testing.T) {
	ec, err := NewFakeEvictionController(t)
	if err != nil {
		klog.Errorf("get new fake cnr lifecycle err %v", err)
	}

	node, err := ec.nodeLister.Get("node1")
	if err != nil {
		klog.Errorf("Get node1 %v error:%v", node, err)
	}

	ec.cnrTaintQueue.Add(node.Name, string(node.UID))
	ec.doTaint()

	ec.cnrEvictQueue.Add(node.Name, string(node.UID))
	ec.doEviction()
}

func TestMonitorAgentHealth(t *testing.T) {
	ec, err := NewFakeEvictionController(t)
	if err != nil {
		klog.Errorf("get new fake cnr lifecycle err %v", err)
	}

	node1, err := ec.nodeLister.Get("node1")
	if err != nil {
		klog.Errorf("Get node1 %v error:%v", node1, err)
	}

	ec.tryUpdateCNRHeartBeatMap()
	if ec.cnrHeartBeatMap.nodeHealths["node1"] == nil {
		t.Errorf("try update cnr heartbeat error")
	}

	cnr, err := ec.cnrLister.Get("node1")
	if err != nil {
		klog.Errorf("Get node1 %v error:%v", node1, err)
	}

	if !ec.checkCNRAgentReady(cnr, 5*time.Second) {
		t.Errorf("check cnr condition error")
	}

	if ok, err := ec.tryUpdateCNRCondition(cnr); err != nil || !ok {
		t.Errorf("try update cnr condition error: %v", err)
	}

	ec.syncAgentHealth()

	return
}

func TestComputeClusterState(t *testing.T) {
	ec, err := NewFakeEvictionController(t)
	if err != nil {
		klog.Errorf("get new fake cnr lifecycle err %v", err)
	}
	state := ec.computeClusterState(0, 2)
	if state != stateFullDisruption {
		t.Errorf("compute cluster state error")
	}

	state = ec.computeClusterState(3, 3)
	if state != statePartialDisruption {
		t.Errorf("compute cluster state error")
	}

	state = ec.computeClusterState(10, 1)
	if state != stateNormal {
		t.Errorf("compute cluster state error")
	}
}

func TestHandleDisruption(t *testing.T) {
	ec, err := NewFakeEvictionController(t)
	if err != nil {
		klog.Errorf("get new fake cnr lifecycle err %v", err)
	}

	ec.handleDisruption(statePartialDisruption)
}

func TestContainersReady(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "container1"},
			},
		},
		Status: corev1.PodStatus{
			ContainerStatuses: []corev1.ContainerStatus{
				{Ready: true},
			},
		},
	}

	k := native.PodIsReady(pod)
	if !k {
		t.Errorf("compute cluster state error")
	}
}
