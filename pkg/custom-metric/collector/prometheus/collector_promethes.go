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
	"net"
	"net/http"
	"path"
	"sync"
	"time"

	"go.uber.org/atomic"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/informers"
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

const (
	metricNamePromCollectorSyncCosts = "kcmas_collector_sync_costs"

	metricNamePromCollectorScrapeReqCount  = "kcmas_collector_scrape_req_cnt"
	metricNamePromCollectorScrapeItemCount = "kcmas_collector_scrape_item_cnt"
	metricNamePromCollectorScrapeLatency   = "kcmas_collector_scrape_latency"

	metricNamePromCollectorStoreReqCount  = "kcmas_collector_store_req_cnt"
	metricNamePromCollectorStoreItemCount = "kcmas_collector_store_item_cnt"
	metricNamePromCollectorStoreLatency   = "kcmas_collector_store_latency"

	fileNameUsername = "username"
	fileNamePassword = "password"
)

// prometheusCollector implements MetricCollector using self-defined parser functionality
// for prometheus formatted contents, and sends to store will standard formats.
// todo: if we restarts, we may lose some metric since the collecting logic interrupts,
// and we need to consider a more reliable way to handle this.
type prometheusCollector struct {
	ctx         context.Context
	collectConf *metric.CollectorConfiguration
	genericConf *metric.GenericMetricConfiguration

	client   *http.Client
	username string
	password string

	emitter     metrics.MetricEmitter
	metricStore store.MetricStore

	podFactory  informers.SharedInformerFactory
	nodeFactory informers.SharedInformerFactory

	podLister  corelisters.PodLister
	nodeLister corelisters.NodeLister

	syncedFunc  []cache.InformerSynced
	syncSuccess bool

	// scrapes maps pod identifier (namespace/name) to its scrapManager,
	// and the scrapManager will use port as unique keys.
	sync.Mutex
	scrapes map[string]*ScrapeManager
}

var _ collector.MetricCollector = &prometheusCollector{}

func NewPrometheusCollector(ctx context.Context, baseCtx *katalystbase.GenericContext, genericConf *metric.GenericMetricConfiguration,
	collectConf *metric.CollectorConfiguration, metricStore store.MetricStore) (collector.MetricCollector, error) {
	client, err := newPrometheusClient()
	if err != nil {
		return nil, fmt.Errorf("creating HTTP client failed: %v", err)
	}

	username, password := extractCredential(collectConf.CredentialPath)

	// since collector will define its own pod/node label selectors, so we will construct informer separately
	klog.Infof("enabled with pod selector: %v, node selector: %v", collectConf.PodSelector.String(), collectConf.NodeSelector.String())
	podFactory := informers.NewSharedInformerFactoryWithOptions(baseCtx.Client.KubeClient, time.Hour*24,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = collectConf.PodSelector.String()
		}))
	podInformer := podFactory.Core().V1().Pods()

	nodeFactory := informers.NewSharedInformerFactoryWithOptions(baseCtx.Client.KubeClient, time.Hour*24,
		informers.WithTweakListOptions(func(options *metav1.ListOptions) {
			options.LabelSelector = collectConf.NodeSelector.String()
		}))
	nodeInformer := nodeFactory.Core().V1().Nodes()

	p := &prometheusCollector{
		ctx:         ctx,
		genericConf: genericConf,
		collectConf: collectConf,
		podFactory:  podFactory,
		nodeFactory: nodeFactory,
		podLister:   podInformer.Lister(),
		nodeLister:  nodeInformer.Lister(),
		syncedFunc: []cache.InformerSynced{
			podInformer.Informer().HasSynced,
			nodeInformer.Informer().HasSynced,
		},
		client:      client,
		username:    username,
		password:    password,
		emitter:     baseCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags("prom_collector"),
		scrapes:     make(map[string]*ScrapeManager),
		syncSuccess: false,
		metricStore: metricStore,
	}

	podInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: func(obj interface{}) bool {
			switch t := obj.(type) {
			case *v1.Pod:
				return p.collectConf.PodSelector.Matches(labels.Set(t.Labels))
			case cache.DeletedFinalStateUnknown:
				if pod, ok := t.Obj.(*v1.Pod); ok {
					return p.collectConf.PodSelector.Matches(labels.Set(pod.Labels))
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

	podFactory.Start(ctx.Done())
	nodeFactory.Start(ctx.Done())

	return p, nil
}

func (p *prometheusCollector) Name() string { return MetricCollectorNamePrometheus }

func (p *prometheusCollector) Start() error {
	p.podFactory.Start(p.ctx.Done())
	p.nodeFactory.Start(p.ctx.Done())
	klog.Info("starting scrape prometheus to collect contents")
	if !cache.WaitForCacheSync(p.ctx.Done(), p.syncedFunc...) {
		return fmt.Errorf("unable to scrape caches for %s", MetricCollectorNamePrometheus)
	}
	klog.Info("started scrape prometheus to collect contents")
	p.syncSuccess = true

	go wait.Until(p.sync, p.collectConf.SyncInterval, p.ctx.Done())
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
		pod.Name, native.PodIsReady(pod), p.collectConf.PodSelector.Matches(labels.Set(pod.Labels)), p.checkTargetNode(node))

	return native.PodIsReady(pod) && p.collectConf.PodSelector.Matches(labels.Set(pod.Labels)) && p.checkTargetNode(node)
}

// checkTargetNode checks whether the given node is targeted
// for metric scrapping logic.
func (p *prometheusCollector) checkTargetNode(node *v1.Node) bool {
	return node != nil && native.NodeReady(node) && p.collectConf.NodeSelector.Matches(labels.Set(node.Labels))
}

// reviseRequest is used to maintain requests based on current status
func (p *prometheusCollector) reviseRequest() {
	klog.Info("revise requests for requests")
	candidatePods, err := p.podLister.List(p.collectConf.PodSelector)
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
	if _, ok := p.scrapes[key]; ok {
		return
	}

	port, ok := native.ParseHostPortForPod(pod, native.ContainerMetricPortName)
	if !ok {
		klog.Errorf("get pod %v port failed", key)
		return
	}

	hostIPs, ok := native.GetPodHostIPs(pod)
	if !ok {
		klog.Errorf("get pod %v hostIPs failed", key)
		return
	}

	var targetURL string
	for _, hostIP := range hostIPs {
		url := fmt.Sprintf("[%s]:%d", hostIP, port)
		if conn, err := net.DialTimeout("tcp", url, time.Second*5); err == nil {
			if conn != nil {
				_ = conn.Close()
			}
			klog.Infof("successfully dial for pod %v with url %v", key, url)
			targetURL = fmt.Sprintf(httpMetricURL, hostIP, port)
			break
		} else {
			klog.Errorf("pod %v dial %v failed: %v", key, url, err)
		}
	}
	if len(targetURL) == 0 {
		klog.Errorf("pod %v has no valid url", key)
		return
	}
	klog.Infof("add requests for pod %v with url %v", key, targetURL)

	// todo all ScrapeManager will share the same http connection now,
	//  reconsider whether it's reasonable in production
	s, err := NewScrapeManager(p.ctx, p.genericConf.OutOfDataPeriod, p.client, pod.Spec.NodeName, targetURL,
		p.emitter, p.username, p.password)
	if err != nil {
		klog.Errorf("failed to new http.Request: %v", err)
		return
	}
	s.Start(p.collectConf.SyncInterval)
	p.scrapes[key] = s
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

	if _, ok := p.scrapes[key]; ok {
		klog.Infof("remove requests for pod %v", pod.Name)
		p.scrapes[key].Stop()
		delete(p.scrapes, key)
	}
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
				p.scrapes[key].Stop()
				delete(p.scrapes, key)
			} else {
				klog.Errorf("failed to get pod %v/%v: %s", namespace, name, err)
			}
		}
	}
	_ = p.emitter.StoreInt64(metricNamePromCollectorScrapeReqCount, int64(len(p.scrapes)), metrics.MetricTypeNameRaw, []metrics.MetricTag{
		{Key: "type", Val: "total"},
	}...)
}

// sync syncs buffered data from each ScrapeManager, and put them into store
func (p *prometheusCollector) sync() {
	var scrapeManagers []*ScrapeManager
	p.Lock()
	for _, s := range p.scrapes {
		scrapeManagers = append(scrapeManagers, s)
	}
	p.Unlock()

	syncStart := time.Now()
	defer func() {
		costs := time.Since(syncStart)
		klog.Infof("prom collector handled with total %v requests, cost %s", len(scrapeManagers), costs.String())
		_ = p.emitter.StoreInt64(metricNamePromCollectorSyncCosts, costs.Microseconds(), metrics.MetricTypeNameRaw)
	}()

	var (
		successReqs = atomic.NewInt64(0)
		failedReqs  = atomic.NewInt64(0)
	)
	handler := func(d []*data.MetricSeries, tags ...metrics.MetricTag) error {
		storeStart := time.Now()
		defer func() {
			_ = p.emitter.StoreInt64(metricNamePromCollectorStoreLatency, time.Since(storeStart).Microseconds(), metrics.MetricTypeNameRaw, tags...)
		}()

		if err := p.metricStore.InsertMetric(d); err != nil {
			failedReqs.Inc()
			return err
		}

		successReqs.Inc()
		return nil
	}
	scrape := func(i int) {
		scrapeManagers[i].HandleMetric(handler)
	}
	workqueue.ParallelizeUntil(p.ctx, general.Max(32, len(scrapeManagers)/64), len(scrapeManagers), scrape)

	klog.Infof("prom collector handle %v succeeded requests, %v failed requests", successReqs.Load(), failedReqs.Load())
	_ = p.emitter.StoreInt64(metricNamePromCollectorStoreReqCount, successReqs.Load(), metrics.MetricTypeNameCount, []metrics.MetricTag{
		{Key: "type", Val: "succeeded"},
	}...)
	_ = p.emitter.StoreInt64(metricNamePromCollectorStoreReqCount, failedReqs.Load(), metrics.MetricTypeNameCount, []metrics.MetricTag{
		{Key: "type", Val: "failed"},
	}...)
}

// extractCredential get username and password from the credential directory
func extractCredential(credentialDir string) (string, string) {
	usernameFilePath := path.Join(credentialDir, fileNameUsername)
	username, usernameErr := extractCredentialFile(usernameFilePath)
	if usernameErr != nil {
		general.Warningf("get username failed, err:%v", usernameErr)
		return "", ""
	}

	passwordFilePath := path.Join(credentialDir, fileNamePassword)
	password, passwordErr := extractCredentialFile(passwordFilePath)
	if passwordErr != nil {
		general.Warningf("get password failed, err:%v", passwordErr)
		return "", ""
	}

	return username, password
}

func extractCredentialFile(filePath string) (string, error) {
	FileExists := general.IsPathExists(filePath)
	if !FileExists {
		return "", fmt.Errorf("file %v does not exist", filePath)
	}

	lines, err := general.ReadFileIntoLines(filePath)
	if err != nil {
		return "", fmt.Errorf("read username file failed, err:%v", err)
	}
	if len(lines) != 1 {
		return "", fmt.Errorf("username is more than 1 line which is unexpected")
	}
	return lines[0], nil
}
