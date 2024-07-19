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

package loadaware

import (
	"context"
	"fmt"
	"hash/crc32"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	listersv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsclientset "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	indicator_plugin "github.com/kubewharf/katalyst-core/pkg/controller/npd/indicator-plugin"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func init() {
	indicator_plugin.RegisterPluginInitializer(LoadAwarePluginName, NewLoadAwarePlugin)
}

type Plugin struct {
	sync.RWMutex

	ctx     context.Context
	workers int32

	nodeLister      listersv1.NodeLister
	npdLister       nodev1alpha1.NodeProfileDescriptorLister
	metricsClient   metricsclientset.Interface
	namespaceLister listersv1.NamespaceLister
	podLister       listersv1.PodLister
	npdUpdater      indicator_plugin.IndicatorUpdater

	nodePoolMap     map[int32]sets.String
	nodeStatDataMap map[string]*NodeMetricData
	podStatDataMap  map[string]*PodMetricData
	nodeToPodsMap   map[string]map[string]struct{}

	syncMetricInterval time.Duration
	listMetricTimeout  time.Duration
	syncedFunc         []cache.InformerSynced

	maxPodUsageCount          int
	enableSyncPodUsage        bool
	podUsageSelectorKey       string
	podUsageSelectorVal       string
	podUsageSelectorNamespace string

	emitter metrics.MetricEmitter
}

func NewLoadAwarePlugin(ctx context.Context, conf *controller.NPDConfig, extraConf interface{},
	controlCtx *katalystbase.GenericContext, updater indicator_plugin.IndicatorUpdater,
) (indicator_plugin.IndicatorPlugin, error) {
	p := &Plugin{
		ctx:     ctx,
		workers: int32(conf.Workers),

		nodeLister:      controlCtx.KubeInformerFactory.Core().V1().Nodes().Lister(),
		npdLister:       controlCtx.InternalInformerFactory.Node().V1alpha1().NodeProfileDescriptors().Lister(),
		podLister:       controlCtx.KubeInformerFactory.Core().V1().Pods().Lister(),
		namespaceLister: controlCtx.KubeInformerFactory.Core().V1().Namespaces().Lister(),
		metricsClient:   controlCtx.Client.MetricClient,
		npdUpdater:      updater,

		nodePoolMap:     make(map[int32]sets.String),
		nodeStatDataMap: make(map[string]*NodeMetricData),
		podStatDataMap:  make(map[string]*PodMetricData),
		nodeToPodsMap:   make(map[string]map[string]struct{}),

		emitter:            controlCtx.EmitterPool.GetDefaultMetricsEmitter(),
		syncMetricInterval: conf.SyncMetricInterval,
		listMetricTimeout:  conf.ListMetricTimeout,
		syncedFunc:         []cache.InformerSynced{},

		maxPodUsageCount:          conf.MaxPodUsageCount,
		podUsageSelectorNamespace: conf.PodUsageSelectorNamespace,
		podUsageSelectorKey:       conf.PodUsageSelectorKey,
		podUsageSelectorVal:       conf.PodUsageSelectorVal,
	}

	nodeInformer := controlCtx.KubeInformerFactory.Core().V1().Nodes().Informer()
	nodeInformer.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    p.OnNodeAdd,
			UpdateFunc: p.OnNodeUpdate,
			DeleteFunc: p.OnNodeDelete,
		},
	)

	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods().Informer()
	podInformer.AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			AddFunc:    p.OnPodAdd,
			UpdateFunc: p.OnPodUpdate,
			DeleteFunc: p.OnPodDelete,
		},
	)

	nsInformer := controlCtx.KubeInformerFactory.Core().V1().Namespaces().Informer()
	nsInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{})

	npdInformer := controlCtx.InternalInformerFactory.Node().V1alpha1().NodeProfileDescriptors().Informer()
	npdInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{})

	p.syncedFunc = []cache.InformerSynced{
		nodeInformer.HasSynced,
		podInformer.HasSynced,
		nsInformer.HasSynced,
		npdInformer.HasSynced,
	}

	return p, nil
}

func (p *Plugin) Run() {
	defer utilruntime.HandleCrash()
	defer func() {
		klog.Infof("Shutting down %s npd plugin", LoadAwarePluginName)
	}()

	if !cache.WaitForCacheSync(p.ctx.Done(), p.syncedFunc...) {
		utilruntime.HandleError(fmt.Errorf("unable to sync caches for %s npd plugin", LoadAwarePluginName))
		return
	}

	klog.Infof("caches are synced for %s controller", LoadAwarePluginName)

	p.Lock()
	defer p.Unlock()

	if p.podUsageRequired() {
		p.enableSyncPodUsage = true
	}

	nodes, err := p.nodeLister.List(labels.Everything())
	if err != nil {
		klog.Fatalf("get all nodes from cache error, err:%v", err)
	}
	// init worker node pool
	for _, node := range nodes {
		bucketID := p.getBucketID(node.Name)
		if pool, ok := p.nodePoolMap[bucketID]; !ok {
			p.nodePoolMap[bucketID] = sets.NewString(node.Name)
		} else {
			pool.Insert(node.Name)
		}
	}

	p.constructNodeToPodMap()

	// restore npd from api server
	p.restoreNPD()

	// start sync node
	go wait.Until(p.syncNode, p.syncMetricInterval, p.ctx.Done())

	go time.AfterFunc(TransferToCRStoreTime, func() {
		klog.Infof("start transferMetaToCRStore")
		wait.Until(func() {
			p.transferMetaToCRStore()
		}, TransferToCRStoreTime, p.ctx.Done())
	})

	go wait.Until(p.podWorker, p.syncMetricInterval, p.ctx.Done())

	go wait.Until(p.reCleanPodData, 5*time.Minute, p.ctx.Done())

	go wait.Until(p.checkPodUsageRequired, time.Minute, p.ctx.Done())
}

func (p *Plugin) Name() string {
	return LoadAwarePluginName
}

func (p *Plugin) GetSupportedNodeMetricsScope() []string {
	return []string{loadAwareMetricsScope, loadAwareMetricMetadataScope}
}

func (p *Plugin) GetSupportedPodMetricsScope() []string {
	return []string{loadAwareMetricsScope}
}

func (p *Plugin) syncNode() {
	// list node metrics
	nodeMetricsMap, err := p.listNodeMetrics()
	if err != nil {
		klog.Errorf("list node metrics fail: %v", err)
		return
	}

	wg := sync.WaitGroup{}
	for i := int32(0); i < p.workers; i++ {
		wg.Add(1)
		go func(id int32) {
			p.worker(id, nodeMetricsMap)
			wg.Done()
		}(i)
	}
	wg.Wait()
}

func (p *Plugin) restoreNPD() {
	if p.nodeStatDataMap == nil {
		p.nodeStatDataMap = make(map[string]*NodeMetricData)
	}
	npds, err := p.npdLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("get all npd from cache fail: %v", err)
		return
	}

	for _, npd := range npds {
		for i := range npd.Status.NodeMetrics {
			if npd.Status.NodeMetrics[i].Scope != loadAwareMetricMetadataScope {
				continue
			}

			var (
				avg15MinCache = make([]*ResourceListWithTime, 0)
				max1HourCache = make([]*ResourceListWithTime, 0)
				max1DayCache  = make([]*ResourceListWithTime, 0)

				avg15MinMap = make(map[metav1.Time]*ResourceListWithTime)
				max1HourMap = make(map[metav1.Time]*ResourceListWithTime)
				max1DayMap  = make(map[metav1.Time]*ResourceListWithTime)
			)

			for _, metricValue := range npd.Status.NodeMetrics[i].Metrics {
				if metricValue.Window.Duration == 15*time.Minute {
					if _, ok := avg15MinMap[metricValue.Timestamp]; !ok {
						avg15MinMap[metricValue.Timestamp] = &ResourceListWithTime{
							Ts:           metricValue.Timestamp.Unix(),
							ResourceList: corev1.ResourceList{},
						}
						avg15MinMap[metricValue.Timestamp].ResourceList[corev1.ResourceName(metricValue.MetricName)] = metricValue.Value
					}
				} else if metricValue.Window.Duration == time.Hour {
					if _, ok := max1HourMap[metricValue.Timestamp]; !ok {
						max1HourMap[metricValue.Timestamp] = &ResourceListWithTime{
							Ts:           metricValue.Timestamp.Unix(),
							ResourceList: corev1.ResourceList{},
						}
					}
					max1HourMap[metricValue.Timestamp].ResourceList[corev1.ResourceName(metricValue.MetricName)] = metricValue.Value
				} else if metricValue.Window.Duration == 24*time.Hour {
					if _, ok := max1DayMap[metricValue.Timestamp]; !ok {
						max1DayMap[metricValue.Timestamp] = &ResourceListWithTime{
							Ts:           metricValue.Timestamp.Unix(),
							ResourceList: corev1.ResourceList{},
						}
					}
					max1DayMap[metricValue.Timestamp].ResourceList[corev1.ResourceName(metricValue.MetricName)] = metricValue.Value
				} else {
					klog.Warningf("unkonw metadata metricName: %v, window: %v", metricValue.MetricName, metricValue.Window)
				}
			}

			for i := range avg15MinMap {
				avg15MinCache = append(avg15MinCache, avg15MinMap[i])
			}
			sort.Sort(ResourceListWithTimeList(avg15MinCache))
			for i := range max1HourMap {
				max1HourCache = append(max1HourCache, max1HourMap[i])
			}
			sort.Sort(ResourceListWithTimeList(max1HourCache))
			for i := range max1DayMap {
				max1DayCache = append(max1DayCache, max1DayMap[i])
			}
			sort.Sort(ResourceListWithTimeList(max1DayCache))

			p.nodeStatDataMap[npd.Name] = &NodeMetricData{
				Latest15MinCache: ResourceListWithTimeList(avg15MinCache).ToResourceList(),
				Latest1HourCache: max1HourCache,
				Latest1DayCache:  max1DayCache,
			}

			break
		}
	}
}

func (p *Plugin) listNodeMetrics() (map[string]*v1beta1.NodeMetrics, error) {
	timeout, cancel := context.WithTimeout(p.ctx, p.listMetricTimeout)
	defer cancel()

	nodeMetricsList, err := p.metricsClient.MetricsV1beta1().NodeMetricses().List(timeout, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	res := make(map[string]*v1beta1.NodeMetrics)
	for _, nm := range nodeMetricsList.Items {
		res[nm.Name] = nm.DeepCopy()
	}

	return res, nil
}

func (p *Plugin) worker(i int32, nodeMetricsMap map[string]*v1beta1.NodeMetrics) {
	p.RLock()
	nodeNames, ok := p.nodePoolMap[i]
	p.RUnlock()

	if !ok {
		return
	}
	for name := range nodeNames {
		nodeMetrics, ok := nodeMetricsMap[name]
		if !ok {
			klog.Errorf("%s node metrics miss", name)
			continue
		}
		now := metav1.Now()
		if isNodeMetricsExpired(nodeMetrics, now) {
			klog.Errorf("node %s node metrics expired, metricsTime: %v", name, nodeMetrics.Timestamp.String())
			continue
		}

		p.Lock()
		nodeMetricData, exist := p.nodeStatDataMap[name]
		if !exist {
			nodeMetricData = &NodeMetricData{}
			p.nodeStatDataMap[name] = nodeMetricData
		}
		// build podUsage
		podUsage := make(map[string]corev1.ResourceList)
		if pods, ok := p.nodeToPodsMap[name]; ok {
			for podNamespaceName := range pods {
				if podMetaData, exist := p.podStatDataMap[podNamespaceName]; exist {
					podUsage[podNamespaceName] = podMetaData.Avg5Min.DeepCopy()
				}
			}
		}
		p.Unlock()
		refreshNodeMetricData(nodeMetricData, nodeMetrics, now.Time)
		err := p.updateNPDStatus(nodeMetricData, name, now, podUsage)
		if err != nil {
			klog.Errorf("createOrUpdateNodeMonitorStatus fail, node: %v, err: %v", name, err)
			continue
		}
	}
}

func (p *Plugin) podWorker() {
	if !p.enableSyncPodUsage {
		return
	}
	nsList, err := p.namespaceLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("get all namespaces failed, err:%v", err)
		return
	}
	for _, ns := range nsList {
		podMetricsList, err := p.getPodMetrics(ns.Name)
		if err != nil {
			klog.Errorf("get podMetrics of namespace:%s failed, err:%v", ns.Name, err)
			continue
		}
		p.Lock()
		for _, podMetrics := range podMetricsList.Items {
			now := metav1.Now()
			if isPodMetricsExpired(&podMetrics, now) {
				klog.Errorf("podMetrics is expired, podName: %v", podMetrics.Name)
				continue
			}
			namespacedName := native.GenerateNamespaceNameKey(podMetrics.Namespace, podMetrics.Name)
			metricData, exist := p.podStatDataMap[namespacedName]
			if !exist {
				metricData = &PodMetricData{}
				p.podStatDataMap[namespacedName] = metricData
			}

			refreshPodMetricData(metricData, &podMetrics)
		}
		p.Unlock()
	}
}

func (p *Plugin) getPodMetrics(namespace string) (*v1beta1.PodMetricsList, error) {
	timeout, cancel := context.WithTimeout(p.ctx, p.listMetricTimeout)
	defer cancel()
	mc := p.metricsClient.MetricsV1beta1()
	return mc.PodMetricses(namespace).List(timeout, metav1.ListOptions{})
}

func (p *Plugin) transferMetaToCRStore() {
	copyNodeStatDataMap := p.getNodeStatDataMap()
	for nodeName, metricData := range copyNodeStatDataMap {
		_, err := p.npdLister.Get(nodeName)
		if err != nil {
			klog.Errorf("get node %v npd fail: %v", nodeName, err)
			continue
		}

		status := &v1alpha1.NodeProfileDescriptorStatus{
			NodeMetrics: make([]v1alpha1.ScopedNodeMetrics, 0),
		}
		p.updateMetadata(status, metricData)

		p.npdUpdater.UpdateNodeMetrics(nodeName, status.NodeMetrics)
		klog.V(10).Infof("update loadAware metadata success, nodeName: %v, data: %v", nodeName, status.NodeMetrics)
	}
}

func (p *Plugin) getNodeStatDataMap() map[string]*NodeMetricData {
	p.RLock()
	defer p.RUnlock()
	meta := make(map[string]*NodeMetricData)
	for nodeName, value := range p.nodeStatDataMap {
		meta[nodeName] = value
	}
	return meta
}

func (p *Plugin) updateNPDStatus(metricData *NodeMetricData, nodeName string, now metav1.Time, podUsages map[string]corev1.ResourceList) error {
	_, err := p.npdLister.Get(nodeName)
	if err != nil {
		err = fmt.Errorf("get node %v npd fail: %v", nodeName, err)
		return err
	}

	npdStatus := &v1alpha1.NodeProfileDescriptorStatus{
		NodeMetrics: make([]v1alpha1.ScopedNodeMetrics, 0),
		PodMetrics:  make([]v1alpha1.ScopedPodMetrics, 0),
	}

	// update node metrics
	p.updateNodeMetrics(npdStatus, metricData, now)

	// update pod metrics if needed
	if p.enableSyncPodUsage {
		p.updatePodMetrics(npdStatus, podUsages, p.maxPodUsageCount)
	}

	// update by updater
	p.npdUpdater.UpdateNodeMetrics(nodeName, npdStatus.NodeMetrics)
	if p.enableSyncPodUsage {
		p.npdUpdater.UpdatePodMetrics(nodeName, npdStatus.PodMetrics)
	}

	klog.V(6).Infof("plugin %v update node %v NPDStatus: %v", LoadAwarePluginName, nodeName, *npdStatus)
	return nil
}

func (p *Plugin) updateMetadata(npdStatus *v1alpha1.NodeProfileDescriptorStatus, metricData *NodeMetricData) {
	metricData.lock.RLock()
	defer metricData.lock.RUnlock()

	metricMetadata := make([]v1alpha1.MetricValue, 0)

	metricMetadata = append(metricMetadata, p.appendMetricValues(metricData.Latest15MinCache, v1alpha1.AggregatorAvg, 15*time.Minute, time.Minute)...)
	metricMetadata = append(metricMetadata, p.appendMetricValuesWithTime(metricData.Latest1HourCache, v1alpha1.AggregatorMax, time.Hour)...)
	metricMetadata = append(metricMetadata, p.appendMetricValuesWithTime(metricData.Latest1DayCache, v1alpha1.AggregatorMax, 24*time.Hour)...)

	npdStatus.NodeMetrics = append(npdStatus.NodeMetrics, v1alpha1.ScopedNodeMetrics{
		Scope:   loadAwareMetricMetadataScope,
		Metrics: metricMetadata,
	})
}

func (p *Plugin) updateNodeMetrics(npdStatus *v1alpha1.NodeProfileDescriptorStatus, metricData *NodeMetricData, now metav1.Time) {
	metricData.lock.RLock()
	defer metricData.lock.RUnlock()

	nodeMetrics := make([]v1alpha1.MetricValue, 0)
	nodeMetrics = append(nodeMetrics, p.appendMetricValue(metricData.Avg5Min, v1alpha1.AggregatorAvg, 5*time.Minute, &now)...)
	nodeMetrics = append(nodeMetrics, p.appendMetricValue(metricData.Avg15Min, v1alpha1.AggregatorAvg, 15*time.Minute, &now)...)
	nodeMetrics = append(nodeMetrics, p.appendMetricValue(metricData.Max1Hour, v1alpha1.AggregatorMax, time.Hour, &now)...)
	nodeMetrics = append(nodeMetrics, p.appendMetricValue(metricData.Max1Day, v1alpha1.AggregatorMax, 24*time.Hour, &now)...)

	npdStatus.NodeMetrics = append(npdStatus.NodeMetrics, v1alpha1.ScopedNodeMetrics{
		Scope:   loadAwareMetricsScope,
		Metrics: nodeMetrics,
	})
}

func (p *Plugin) updatePodMetrics(npdStatus *v1alpha1.NodeProfileDescriptorStatus, podUsage map[string]corev1.ResourceList, maxPodUsageCount int) {
	podUsage = getTopNPodUsages(podUsage, maxPodUsageCount)

	podMetrics := make([]v1alpha1.PodMetric, 0)
	for namespaceName, resourceList := range podUsage {
		podMetric, err := p.appendPodMetrics(resourceList, namespaceName)
		if err != nil {
			klog.Errorf("skip pod: %v, update pod metrics fail: %v", namespaceName, err)
			continue
		}
		podMetrics = append(podMetrics, podMetric)
	}

	npdStatus.PodMetrics = append(npdStatus.PodMetrics, v1alpha1.ScopedPodMetrics{
		Scope:      loadAwareMetricsScope,
		PodMetrics: podMetrics,
	})
}

func (p *Plugin) appendMetricValue(resourceList corev1.ResourceList, aggregator v1alpha1.Aggregator, window time.Duration, now *metav1.Time) []v1alpha1.MetricValue {
	metricValues := make([]v1alpha1.MetricValue, 0)
	for resourceName, quantity := range resourceList {
		mv := v1alpha1.MetricValue{
			MetricName: resourceName.String(),
			Window:     &metav1.Duration{Duration: window},
			Aggregator: &aggregator,
			Value:      quantity,
		}
		if now != nil {
			mv.Timestamp = *now
		}
		metricValues = append(metricValues, mv)
	}
	return metricValues
}

func (p *Plugin) appendMetricValues(resourceLists []corev1.ResourceList, aggregator v1alpha1.Aggregator, window time.Duration, step time.Duration) []v1alpha1.MetricValue {
	metricValues := make([]v1alpha1.MetricValue, 0)
	ts := time.Now().Add(-1 * window)
	for _, resourceList := range resourceLists {
		metricValues = append(metricValues, p.appendMetricValue(resourceList, aggregator, window, &metav1.Time{Time: ts})...)
		ts = ts.Add(step)
	}

	return metricValues
}

func (p *Plugin) appendMetricValuesWithTime(resourceLists []*ResourceListWithTime, aggregator v1alpha1.Aggregator, window time.Duration) []v1alpha1.MetricValue {
	metricValues := make([]v1alpha1.MetricValue, 0)
	for _, resourceListWithTime := range resourceLists {
		ts := metav1.Time{Time: time.Unix(resourceListWithTime.Ts, 0)}
		metricValues = append(metricValues, p.appendMetricValue(resourceListWithTime.ResourceList, aggregator, window, &ts)...)
	}
	return metricValues
}

func (p *Plugin) appendPodMetrics(resourceList corev1.ResourceList, namespaceName string) (v1alpha1.PodMetric, error) {
	namespace, name, err := native.ParseNamespaceNameKey(namespaceName)
	if err != nil {
		return v1alpha1.PodMetric{}, err
	}

	podMetric := v1alpha1.PodMetric{
		Namespace: namespace,
		Name:      name,
		Metrics:   []v1alpha1.MetricValue{},
	}
	for resourceName, quantity := range resourceList {
		podMetric.Metrics = append(podMetric.Metrics, v1alpha1.MetricValue{
			MetricName: resourceName.String(),
			Value:      quantity,
		})
	}

	return podMetric, nil
}

func (p *Plugin) constructNodeToPodMap() {
	pods, err := p.podLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("list all pod error, err:%v", err)
		return
	}
	for _, pod := range pods {
		if len(pod.Spec.NodeName) > 0 {
			if podMap, ok := p.nodeToPodsMap[pod.Spec.NodeName]; ok {
				podMap[native.GenerateNamespaceNameKey(pod.Namespace, pod.Name)] = struct{}{}
			} else {
				p.nodeToPodsMap[pod.Spec.NodeName] = map[string]struct{}{
					native.GenerateNamespaceNameKey(pod.Namespace, pod.Name): {},
				}
			}
		}
	}
}

func (p *Plugin) podUsageRequired() bool {
	pods, err := p.podLister.Pods(p.podUsageSelectorNamespace).
		List(labels.SelectorFromSet(map[string]string{p.podUsageSelectorKey: p.podUsageSelectorVal}))
	if err != nil {
		klog.Errorf("get pod usage pods err: %v", err)
		return false
	}
	return len(pods) > 0
}

func (p *Plugin) getBucketID(name string) int32 {
	hash := int64(crc32.ChecksumIEEE([]byte(name)))
	size := hash % int64(p.workers)
	return int32(size)
}

// reCleanPodData Because the data in the podStatDataMap is pulled from the metrics-server with a specific interval,
// relying solely on pod delete events to remove data from the podStatDataMap can result in data residue.
// Here we need to actively perform cleanup data residue.
func (p *Plugin) reCleanPodData() {
	p.Lock()
	defer p.Unlock()
	pods, err := p.podLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("get all pods error, err:=%v", err)
		return
	}
	existPod := make(map[string]struct{})
	for _, pod := range pods {
		existPod[native.GenerateNamespaceNameKey(pod.Namespace, pod.Name)] = struct{}{}
	}
	for podName := range p.podStatDataMap {
		if _, ok := existPod[podName]; !ok {
			delete(p.podStatDataMap, podName)
		}
	}
}

func (p *Plugin) checkPodUsageRequired() {
	if p.podUsageRequired() {
		podUsageUnrequiredCount = 0
		p.enableSyncPodUsage = true
	} else {
		podUsageUnrequiredCount++
		if podUsageUnrequiredCount >= 5 {
			p.enableSyncPodUsage = false
			podUsageUnrequiredCount = 0
		}
	}
}
