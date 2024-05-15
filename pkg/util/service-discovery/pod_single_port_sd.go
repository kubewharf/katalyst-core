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

package service_discovery

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func init() { RegisterSDManagerInitializers(ServiceDiscoveryPodSinglePort, NewPodSinglePortSDManager) }

const ServiceDiscoveryPodSinglePort = "pod-single-port"

const (
	defaultPodReSyncPeriod = time.Hour * 24
	defaultPodSyncPeriod   = time.Second * 3
)

type podSinglePortSDManager struct {
	sync.RWMutex
	endpoints map[string]string

	portName   string
	ctx        context.Context
	podLister  corelisters.PodLister
	syncedFunc cache.InformerSynced
}

func NewPodSinglePortSDManager(ctx context.Context, agentCtx *katalystbase.GenericContext,
	conf *generic.ServiceDiscoveryConf,
) (ServiceDiscoveryManager, error) {
	klog.Infof("%v sd manager enabled with pod selector: %v", ServiceDiscoveryPodSinglePort, conf.PodLister.String())
	podFactory := informers.NewSharedInformerFactoryWithOptions(agentCtx.Client.KubeClient, defaultPodReSyncPeriod,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = conf.PodLister.String()
		}))
	podInformer := podFactory.Core().V1().Pods()

	m := &podSinglePortSDManager{
		portName:   conf.PodSinglePortSDConf.PortName,
		ctx:        ctx,
		endpoints:  make(map[string]string),
		podLister:  podInformer.Lister(),
		syncedFunc: podInformer.Informer().HasSynced,
	}

	podInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch t := obj.(type) {
			case *v1.Pod:
				return native.PodIsReady(t)
			case cache.DeletedFinalStateUnknown:
				if pod, ok := t.Obj.(*v1.Pod); ok {
					return native.PodIsReady(pod)
				}
				utilruntime.HandleError(fmt.Errorf("unable to convert object %T to *v1.Pod", obj))
				return false
			default:
				utilruntime.HandleError(fmt.Errorf("unable to handle object: %T", obj))
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    m.addPod,
			UpdateFunc: m.updatePod,
			DeleteFunc: m.deletePod,
		},
	})

	podFactory.Start(ctx.Done())

	return m, nil
}

func (m *podSinglePortSDManager) Name() string { return ServiceDiscoveryPodSinglePort }

func (m *podSinglePortSDManager) GetEndpoints() ([]string, error) {
	m.RLock()
	defer m.RUnlock()

	endpoints := make([]string, 0, len(m.endpoints))
	for _, ep := range m.endpoints {
		endpoints = append(endpoints, ep)
	}

	return endpoints, nil
}

func (m *podSinglePortSDManager) Run() error {
	if !cache.WaitForCacheSync(m.ctx.Done(), m.syncedFunc) {
		return fmt.Errorf("unable to sync caches for podSinglePortSDManager")
	}

	go wait.Until(m.sync, defaultPodSyncPeriod, m.ctx.Done())
	return nil
}

func (m *podSinglePortSDManager) addPod(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", obj)
		return
	}

	klog.V(6).Infof("add pod %v", pod.Name)
	m.addEndpoint(pod)
}

func (m *podSinglePortSDManager) updatePod(_, newObj interface{}) {
	newPod, ok := newObj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", newObj)
		return
	}

	klog.V(6).Infof("update pod %v", newPod.Name)
	m.addEndpoint(newPod)
}

func (m *podSinglePortSDManager) deletePod(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", obj)
		return
	}

	klog.V(6).Infof("delete pod %v", pod.Name)
	m.removeEndpoint(pod)
}

func (m *podSinglePortSDManager) removeEndpoint(pod *v1.Pod) {
	m.Lock()
	defer m.Unlock()

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pod)
	if err != nil {
		klog.Errorf("couldn't get key for pod %#v: %v", pod, err)
		return
	}

	delete(m.endpoints, key)
}

func (m *podSinglePortSDManager) addEndpoint(pod *v1.Pod) {
	m.Lock()
	defer m.Unlock()

	m.addEndpointWithoutLock(pod)
}

func (m *podSinglePortSDManager) addEndpointWithoutLock(pod *v1.Pod) {
	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pod)
	if err != nil {
		klog.Errorf("couldn't get key for pod %#v: %v", pod, err)
		return
	} else if _, exist := m.endpoints[key]; exist {
		return
	}

	endpoint, err := m.getPodEndpoint(pod)
	if err != nil {
		klog.ErrorS(err, "get new endpoint failed", "pod", pod.Name)
		return
	}

	klog.Infof("add endpoint %s for pod %v", endpoint, key)

	m.endpoints[key] = endpoint
}

func (m *podSinglePortSDManager) getPodEndpoint(pod *v1.Pod) (string, error) {
	port, ok := native.ParseHostPortForPod(pod, m.portName)
	if !ok {
		return "", fmt.Errorf("pod has invalid valid port：%v", m.portName)
	}

	hostIPs, ok := native.GetPodHostIPs(pod)
	if !ok {
		return "", fmt.Errorf("pod has invalid valid host-ip")
	}

	for _, hostIP := range hostIPs {
		url := fmt.Sprintf("[%s]:%d", hostIP, port)
		if conn, err := net.DialTimeout("tcp", url, time.Second*5); err == nil {
			if conn != nil {
				_ = conn.Close()
			}
			return url, nil
		} else {
			klog.Errorf("pod %v dial %v failed: %v", pod.Name, url, err)
		}
	}

	return "", fmt.Errorf("invalid endpoint exits")
}

func (m *podSinglePortSDManager) sync() {
	m.Lock()
	defer m.Unlock()

	for key := range m.endpoints {
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			klog.Errorf("failed to split namespace and name from key %s", key)
			continue
		}

		if _, err := m.podLister.Pods(namespace).Get(name); err != nil {
			if errors.IsNotFound(err) {
				delete(m.endpoints, key)
			} else {
				klog.Errorf("failed to get pod %s: %s", key, err)
			}
		}
	}

	pods, err := m.podLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("failed to list pods: %v", err)
		return
	}
	for _, pod := range pods {
		m.addEndpointWithoutLock(pod)
	}
}
