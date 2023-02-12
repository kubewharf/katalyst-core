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

package remote

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"k8s.io/api/core/v1"
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
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const httpMetricURL = "http://%v:%v"

// ShardingController is responsible to separate the metric store into
// several sharding pieces to tolerant single node failure, as well as
// avoiding memory pressure in single node.
//
// todo: currently, it not really a valid
type ShardingController struct {
	ctx context.Context

	sync.Mutex
	requests   map[string]string
	totalCount int

	podSelector labels.Selector
	podLister   corelisters.PodLister
	syncedFunc  []cache.InformerSynced
}

func NewShardingController(ctx context.Context, baseCtx *katalystbase.GenericContext, podSelector labels.Selector, totalCount int) *ShardingController {
	// since collector will define its own pod/node label selectors, so we will construct informer separately
	klog.Infof("enabled with pod selector: %v", podSelector.String())
	podFactory := informers.NewSharedInformerFactoryWithOptions(baseCtx.Client.KubeClient, time.Hour*24,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = podSelector.String()
		}))
	podInformer := podFactory.Core().V1().Pods()

	s := &ShardingController{
		ctx:         ctx,
		totalCount:  totalCount,
		requests:    make(map[string]string),
		podSelector: podSelector,
		podLister:   podInformer.Lister(),
		syncedFunc: []cache.InformerSynced{
			podInformer.Informer().HasSynced,
		},
	}

	podInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch t := obj.(type) {
			case *v1.Pod:
				return podSelector.Matches(labels.Set(t.Labels))
			case cache.DeletedFinalStateUnknown:
				if pod, ok := t.Obj.(*v1.Pod); ok {
					return podSelector.Matches(labels.Set(pod.Labels))
				}
				utilruntime.HandleError(fmt.Errorf("unable to convert object %T to *v1.Pod", obj))
				return false
			default:
				utilruntime.HandleError(fmt.Errorf("unable to handle object: %T", obj))
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    s.addPod,
			UpdateFunc: s.updatePod,
			DeleteFunc: s.deletePod,
		},
	})

	podFactory.Start(ctx.Done())

	return s
}

func (s *ShardingController) Start() error {
	klog.Info("starting sharding controller with count: %v", s.totalCount)
	if !cache.WaitForCacheSync(s.ctx.Done(), s.syncedFunc...) {
		return fmt.Errorf("unable to sync caches for %s", "sharding controller")
	}
	klog.Info("started sharding controller")

	go wait.Until(s.sync, time.Second*3, s.ctx.Done())
	return nil
}

func (s *ShardingController) Stop() error {
	return nil
}

// GetRWCount returns the quorum read/write counts
func (s *ShardingController) GetRWCount() (int, int) {
	s.Lock()
	defer s.Unlock()

	r := (s.totalCount + 1) / 2
	w := s.totalCount - r + 1
	return r, w
}

// GetRequests returns the pre-generated http requests
func (s *ShardingController) GetRequests(path string) []*http.Request {
	s.Lock()
	defer s.Unlock()

	var requests []*http.Request
	for _, urlPrefix := range s.requests {
		req, err := s.generateRequest(urlPrefix, path)
		if err != nil {
			klog.Errorf("failed to generate request err: %v", err)
		}

		requests = append(requests, req)
	}
	return requests
}

func (s *ShardingController) addPod(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", obj)
		return
	}

	klog.V(6).Infof("add pod %v", pod.Name)
	s.addRequest(pod)
}

func (s *ShardingController) updatePod(_, newObj interface{}) {
	newPod, ok := newObj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", newObj)
		return
	}

	klog.V(6).Infof("update pod %v", newPod.Name)
	s.addRequest(newPod)
}

func (s *ShardingController) deletePod(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", obj)
		return
	}

	klog.V(6).Infof("delete pod %v", pod.Name)
	s.removeRequest(pod)
}

// syncRequestedURL is used to sync requested url from
func (s *ShardingController) sync() {
	s.Lock()
	defer s.Unlock()

	for key := range s.requests {
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			klog.Errorf("failed to split namespace and name from key %s", key)
			continue
		}

		if _, err := s.podLister.Pods(namespace).Get(name); err != nil {
			if errors.IsNotFound(err) {
				delete(s.requests, key)
			} else {
				klog.Errorf("failed to get pod %v/%v: %s", namespace, name, err)
			}
		}
	}
}

// addRequest constructs http.Request based on pod info
func (s *ShardingController) addRequest(pod *v1.Pod) {
	if pod == nil {
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pod)
	if err != nil {
		klog.Errorf("couldn't get key for pod %#v: %v", pod, err)
		return
	}

	s.Lock()
	defer s.Unlock()

	ports := native.ParseHostPortsForPod(pod, native.ContainerMetricStorePortName)
	if len(ports) != 1 {
		klog.Errorf("pod %v has invalid amount of valid ports: %v", key, ports)
		return
	}
	port := ports[0]

	hostIP := pod.Status.HostIP
	if len(hostIP) == 0 {
		klog.Errorf("pod %v has empty hostIP", key)
		return
	}

	url := fmt.Sprintf(httpMetricURL, pod.Status.HostIP, port)

	if originURL, exist := s.requests[key]; exist && originURL == url {
		return
	}

	klog.Infof("add requests for pod %v with url %v", pod.Name, url)
	s.requests[key] = url
}

// addRequest delete http.Request for the given pod
func (s *ShardingController) removeRequest(pod *v1.Pod) {
	s.Lock()
	defer s.Unlock()

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pod)
	if err != nil {
		klog.Errorf("couldn't get key for pod %#v: %v", pod, err)
		return
	}

	klog.Infof("remove requests for pod %v", pod.Name)
	delete(s.requests, key)
}

func (s *ShardingController) generateRequest(urlPrefix, path string) (*http.Request, error) {
	url := urlPrefix + path

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("new http request for %v err: %v", url, err)
	}

	req.Header.Add("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "remote-store")

	switch path {
	case local.ServingGetPath:
		req.Method = "GET"
	case local.ServingSetPath:
		req.Method = "POST"
	case local.ServingListPath:
		req.Method = "GET"
	}

	return req, nil
}
