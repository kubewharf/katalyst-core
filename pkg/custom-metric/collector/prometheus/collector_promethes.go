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

package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/collector"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/data"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const MetricCollectorNamePrometheus = "prometheus-collector"

// prometheusCollector implements MetricCollector using self-defined parser functionality
// for prometheus formatted contents, and sends to store will standard formats.
// todo: if we restarts, we may lose some metric since the collecting logic interrupts
//  need to consider a more reliable way to handle this
type prometheusCollector struct {
	ctx  context.Context
	conf *metric.CollectorConfiguration

	client      *http.Client
	emitter     metrics.MetricEmitter
	metricStore store.MetricStore

	podLister  corelisters.PodLister
	nodeLister corelisters.NodeLister

	syncedFunc  []cache.InformerSynced
	syncSuccess bool

	// scrapes maps pod identifier (namespace/name) to its scrapManager,
	// and the scrapManager will use port as unique keys.
	sync.Mutex
	scrapes map[string]map[int32]*ScrapeManager
}

var _ collector.MetricCollector = &prometheusCollector{}

func NewPrometheusCollector(ctx context.Context, baseCtx *katalystbase.GenericContext,
	conf *metric.CollectorConfiguration, metricStore store.MetricStore) (collector.MetricCollector, error) {
	client, err := newPrometheusClient()
	if err != nil {
		return nil, fmt.Errorf("creating HTTP client failed: %v", err)
	}

	klog.Infof("enabled with pod selector: %v, node selector: %v", conf.PodSelector.String(), conf.NodeSelector.String())
	podInformer := baseCtx.KubeInformerFactory.Core().V1().Pods()
	nodeInformer := baseCtx.KubeInformerFactory.Core().V1().Nodes()
	p := &prometheusCollector{
		ctx:        ctx,
		conf:       conf,
		podLister:  podInformer.Lister(),
		nodeLister: nodeInformer.Lister(),
		syncedFunc: []cache.InformerSynced{
			podInformer.Informer().HasSynced,
			nodeInformer.Informer().HasSynced,
		},
		client:      client,
		emitter:     baseCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags("prom_collector"),
		scrapes:     make(map[string]map[int32]*ScrapeManager),
		syncSuccess: false,
		metricStore: metricStore,
	}

	podInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch t := obj.(type) {
			case *v1.Pod:
				return p.conf.PodSelector.Matches(labels.Set(t.Labels))
			case cache.DeletedFinalStateUnknown:
				if pod, ok := t.Obj.(*v1.Pod); ok {
					return p.conf.PodSelector.Matches(labels.Set(pod.Labels))
				}
				utilruntime.HandleError(fmt.Errorf("unable to convert object %T to *v1.Pod", obj))
				return false
			default:
				utilruntime.HandleError(fmt.Errorf("unable to handle object: %T", obj))
				return false
			}
		},
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    p.addPod,
			UpdateFunc: p.updatePod,
			DeleteFunc: p.deletePod,
		},
	})

	return p, nil
}

func (p *prometheusCollector) Name() string { return MetricCollectorNamePrometheus }

func (p *prometheusCollector) Start() error {
	klog.Info("starting scrape prometheus to collect contents")
	if !cache.WaitForCacheSync(p.ctx.Done(), p.syncedFunc...) {
		return fmt.Errorf("unable to scrape caches for %s", MetricCollectorNamePrometheus)
	}
	klog.Info("started scrape prometheus to collect contents")
	p.syncSuccess = true

	go wait.Until(p.sync, p.conf.SyncInterval, p.ctx.Done())
	go wait.Until(p.reviseRequest, time.Minute*5, p.ctx.Done())
	return nil
}

func (p *prometheusCollector) Stop() error {
	return nil
}

func (p *prometheusCollector) addPod(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", obj)
		return
	}

	if p.checkTargetPod(pod) {
		klog.Info("pod %v added with target scraping", pod.Name)
		p.addRequest(pod)
	}
}

func (p *prometheusCollector) updatePod(oldObj, newObj interface{}) {
	oldPod, ok := oldObj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", oldObj)
		return
	}
	oldMatch := p.checkTargetPod(oldPod)

	newPod, ok := newObj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", newObj)
		return
	}
	newMatch := p.checkTargetPod(newPod)

	if !oldMatch && newMatch {
		klog.Infof("pod %v updated with target scraping", newPod.Name)
		p.addRequest(newPod)
	}
}

func (p *prometheusCollector) deletePod(obj interface{}) {
	pod, ok := obj.(*v1.Pod)
	if !ok {
		klog.ErrorS(nil, "Cannot convert to *v1.Pod", "obj", obj)
		return
	}

	// regardless whether current pod can match up with the logic
	p.removeRequest(pod)
}

// checkTargetPod checks whether the given pod is targeted
// for metric scrapping logic.
func (p *prometheusCollector) checkTargetPod(pod *v1.Pod) bool {
	// if local cache hasn't been synced successfully, just return not matched
	if !p.syncSuccess {
		return false
	}

	if pod == nil || pod.Spec.NodeName == "" {
		return false
	}

	node, err := p.nodeLister.Get(pod.Spec.NodeName)
	if err != nil {
		klog.Errorf("get node %v failed: %v", pod.Spec.NodeName, err)
		return false
	}

	klog.V(6).Infof("check for pod %v: %v, %v, %v",
		pod.Name, native.PodIsReady(pod), p.conf.PodSelector.Matches(labels.Set(pod.Labels)), p.checkTargetNode(node))

	return native.PodIsReady(pod) && p.conf.PodSelector.Matches(labels.Set(pod.Labels)) && p.checkTargetNode(node)
}

// checkTargetNode checks whether the given node is targeted
// for metric scrapping logic.
func (p *prometheusCollector) checkTargetNode(node *v1.Node) bool {
	klog.V(6).Infof("check for node %v: %v, %v, %v",
		node.Name, native.NodeReady(node), p.conf.NodeSelector.Matches(labels.Set(node.Labels)))

	return node != nil && native.NodeReady(node) && p.conf.NodeSelector.Matches(labels.Set(node.Labels))
}

// reviseRequest is used to maintain requests based on current status
func (p *prometheusCollector) reviseRequest() {
	klog.Info("revise requests for requests")
	candidatePods, err := p.podLister.List(p.conf.PodSelector)
	if err != nil {
		klog.Errorf("failed to list pods: %v", err)
		return
	}

	for _, pod := range candidatePods {
		if p.checkTargetPod(pod) {
			p.addRequest(pod)
		}
	}
	p.clearRequests()
}

// addRequest constructs http.Request based on pod info
func (p *prometheusCollector) addRequest(pod *v1.Pod) {
	if pod == nil {
		return
	}

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pod)
	if err != nil {
		klog.Errorf("couldn't get key for pod %#v: %v", pod, err)
		return
	}

	p.Lock()
	defer p.Unlock()
	if p.scrapes[key] == nil {
		p.scrapes[key] = make(map[int32]*ScrapeManager)
	}

	ports := native.ParseHostPortsForPod(pod, native.ContainerMetricPortName)
	for _, port := range ports {
		if _, ok := p.scrapes[key][port]; ok {
			continue
		}

		url := fmt.Sprintf(httpMetricURL, pod.Status.HostIP, port)
		klog.Infof("add requests for pod %v with url %v", pod.Name, url)

		// all ScrapeManager will share the same http connection now,
		// reconsider whether it's reasonable in production
		s, err := NewScrapeManager(p.ctx, p.client, pod.Spec.NodeName, url, p.emitter)
		if err != nil {
			klog.Errorf("failed to new http.Request: %v", err)
			continue
		}

		s.Start(p.conf.SyncInterval)
		p.scrapes[key][port] = s
	}
}

// addRequest delete http.Request for the given pod
func (p *prometheusCollector) removeRequest(pod *v1.Pod) {
	p.Lock()
	defer p.Unlock()

	key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(pod)
	if err != nil {
		klog.Errorf("couldn't get key for pod %#v: %v", pod, err)
		return
	}

	klog.Infof("remove requests for pod %v", pod.Name)
	for _, s := range p.scrapes[key] {
		s.Stop()
	}
	delete(p.scrapes, key)
}

// addRequest delete http.Request for the given pod
func (p *prometheusCollector) clearRequests() {
	p.Lock()
	defer p.Unlock()

	for key := range p.scrapes {
		namespace, name, err := cache.SplitMetaNamespaceKey(key)
		if err != nil {
			klog.Errorf("failed to split namespace and name from key %s", key)
			continue
		}

		if _, err := p.podLister.Pods(namespace).Get(name); err != nil {
			if errors.IsNotFound(err) {
				for _, s := range p.scrapes[key] {
					s.Stop()
				}
				delete(p.scrapes, key)
			} else {
				klog.Errorf("failed to get pod %v/%v: %s", namespace, name, err)
			}
		}
	}
}

// sync syncs buffered data from each ScrapeManager, and put them into store
func (p *prometheusCollector) sync() {
	var scrapeManagers []*ScrapeManager
	p.Lock()
	for _, smap := range p.scrapes {
		for _, s := range smap {
			scrapeManagers = append(scrapeManagers, s)
		}
	}
	p.Unlock()

	klog.Infof("handled with total %v requests", len(scrapeManagers))
	handler := func(d *data.MetricSeries) error {
		return p.metricStore.InsertMetric([]*data.MetricSeries{d})
	}
	scrape := func(i int) {
		scrapeManagers[i].HandleMetric(handler)
	}

	workqueue.ParallelizeUntil(p.ctx, general.Max(32, len(scrapeManagers)/64), len(scrapeManagers), scrape)
}
