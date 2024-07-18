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

package prediction

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	listerv1 "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	workloadlister "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/common"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/predictor"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction/provider/prom"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type Prediction struct {
	sync.RWMutex
	ctx context.Context

	conf *controller.OvercommitConfig

	provider  prom.Interface
	predictor predictor.Interface

	nodeLister     listerv1.NodeLister
	podIndexer     cache.Indexer
	workloadLister map[schema.GroupVersionResource]cache.GenericLister
	spdLister      workloadlister.ServiceProfileDescriptorLister
	syncedFunc     []cache.InformerSynced

	// cache workload usage calculated by predictor when portrait is unable
	// workload -> metricName -> predict timeSeries
	workloadUsageCache map[string]map[string]*common.TimeSeries

	nodeUpdater control.NodeUpdater
	rpUpdater   control.SPDControlImp

	metricsEmitter metrics.MetricEmitter

	firstReconcileWorkload bool
}

func NewPredictionController(
	ctx context.Context,
	controlCtx *katalyst_base.GenericContext,
	overcommitConf *controller.OvercommitConfig,
) (*Prediction, error) {
	if overcommitConf == nil || controlCtx.Client == nil {
		return nil, fmt.Errorf("client and overcommitConf can not be nil")
	}

	klog.V(6).Infof("overcommitConf: %v", *overcommitConf)

	predictionController := &Prediction{
		ctx:                    ctx,
		metricsEmitter:         controlCtx.EmitterPool.GetDefaultMetricsEmitter(),
		conf:                   overcommitConf,
		workloadLister:         map[schema.GroupVersionResource]cache.GenericLister{},
		firstReconcileWorkload: false,
	}

	// init workload lister
	workloadInformers := controlCtx.DynamicResourcesManager.GetDynamicInformers()
	for _, wf := range workloadInformers {
		klog.Infof("workload informer: %v", wf.GVR.String())
		predictionController.workloadLister[wf.GVR] = wf.Informer.Lister()
		predictionController.syncedFunc = append(predictionController.syncedFunc, wf.Informer.Informer().HasSynced)
	}

	err := predictionController.initProvider()
	if err != nil {
		klog.Errorf("init provider fail: %v", err)
		return nil, err
	}

	// init predictor
	err = predictionController.initPredictor()
	if err != nil {
		klog.Errorf("init predictor fail: %v", err)
		return nil, err
	}

	if predictionController.predictor != nil && predictionController.provider != nil {
		// if local predictor is enabled, init workloadUsageCache
		predictionController.workloadUsageCache = map[string]map[string]*common.TimeSeries{}
	} else {
		// if spd portrait is enabled, init spdLister
		predictionController.spdLister = controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Lister()
		predictionController.syncedFunc = append(predictionController.syncedFunc, controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Informer().HasSynced)
	}

	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods()
	predictionController.podIndexer = podInformer.Informer().GetIndexer()
	predictionController.podIndexer.AddIndexers(cache.Indexers{
		nodePodIndex: nodePodIndexFunc,
	})
	predictionController.syncedFunc = append(predictionController.syncedFunc, podInformer.Informer().HasSynced)

	predictionController.nodeLister = controlCtx.KubeInformerFactory.Core().V1().Nodes().Lister()
	predictionController.syncedFunc = append(predictionController.syncedFunc, controlCtx.KubeInformerFactory.Core().V1().Nodes().Informer().HasSynced)

	predictionController.nodeUpdater = control.NewRealNodeUpdater(controlCtx.Client.KubeClient)

	predictionController.addDeleteHandler(workloadInformers)

	return predictionController, nil
}

func (p *Prediction) Run() {
	if !cache.WaitForCacheSync(p.ctx.Done(), p.syncedFunc...) {
		klog.Fatalf("unable to sync caches")
	}

	if p.predictor != nil && p.provider != nil {
		go wait.Until(p.reconcileWorkloads, p.conf.Prediction.PredictPeriod, p.ctx.Done())

		// if predictor is used, wait for first reconcile before reconcile nodes
		_ = wait.PollImmediateUntil(5*time.Second, func() (done bool, err error) {
			return p.firstReconcileWorkload, nil
		}, p.ctx.Done())

		klog.V(6).Infof("first reconcile workloads finish: %v", p.firstReconcileWorkload)

		go wait.Until(p.reconcileNodes, p.conf.Prediction.ReconcilePeriod, p.ctx.Done())
	} else {
		klog.Infof("nil predictor, skip reconcile workload")
		go wait.Until(p.reconcileNodes, p.conf.Prediction.ReconcilePeriod, p.ctx.Done())
	}
}

// calculate node usage and overcommitment ratio by workloads usage
func (p *Prediction) reconcileNodes() {
	// list nodes
	nodeList, err := p.nodeLister.List(labels.Everything())
	if err != nil {
		klog.Errorf("overcommit prediction list node fail: %v", err)
		return
	}

	for _, node := range nodeList {
		cpuVal, memoryVal, err := p.estimateNode(node)
		if err != nil {
			klog.Errorf("estimate node %v fail: %v", node.Name, err)
			continue
		}
		klog.V(6).Infof("estimate node %v overcommit CPU: %v, memory: %v", node.Name, cpuVal, memoryVal)

		// update node annotation
		err = p.updatePredictOvercommitRatio(cpuVal, memoryVal, node)
		if err != nil {
			klog.Errorf("update node %v overcommit ratio fail: %v", node.Name, err)
			continue
		}
	}
}

func (p *Prediction) estimateNode(node *v1.Node) (float64, float64, error) {
	// get pods in node
	objs, err := p.podIndexer.ByIndex(nodePodIndex, node.Name)
	if err != nil {
		return 0, 0, err
	}
	if len(objs) <= 0 {
		return 0, 0, nil
	}

	var (
		sumPodCpuTimeSeries, sumPodMemoryTimeSeries *common.TimeSeries
		nodeResource                                = v1.ResourceList{}
	)
	for _, obj := range objs {
		pod, ok := obj.(*v1.Pod)
		if !ok {
			return 0, 0, fmt.Errorf("can not convert obj to pod: %v", obj)
		}
		klog.V(6).Infof("estimate node, namespace: %v, podname: %v, owner: %v", pod.Namespace, pod.Name, pod.OwnerReferences)

		// get pod resource portrait usage
		cpuTimeSeries, memoryTimeSeries, podResource := p.podResourceTimeSeries(pod)

		// sum pod resources
		if sumPodCpuTimeSeries == nil {
			sumPodCpuTimeSeries = cpuTimeSeries
		} else {
			err = sumPodCpuTimeSeries.Add(cpuTimeSeries)
			if err != nil {
				klog.Errorf("estimate node %v fail: %v", node.Name, err)
				return 0, 0, err
			}
		}
		if sumPodMemoryTimeSeries == nil {
			sumPodMemoryTimeSeries = memoryTimeSeries
		} else {
			err = sumPodMemoryTimeSeries.Add(memoryTimeSeries)
			if err != nil {
				klog.Errorf("estimate node %v fail: %v", node.Name, err)
				return 0, 0, err
			}
		}

		nodeResource = native.AddResources(nodeResource, podResource)
	}

	klog.V(6).Infof("node %v cpu resource: %v, memory resource: %v, pod CPU timeSeries: %v, pod memory timeSeries: %v",
		node.Name, nodeResource.Cpu().String(), nodeResource.Memory().String(), sumPodCpuTimeSeries, sumPodMemoryTimeSeries)

	nodeAllocatable := getNodeAllocatable(node)
	// calculate node overcommitment ratio
	cpuOvercommitRatio := p.resourceToOvercommitRatio(
		node.Name,
		v1.ResourceCPU.String(),
		float64(nodeResource.Cpu().MilliValue()),
		sumPodCpuTimeSeries.Max().Value,
		float64(nodeAllocatable.Cpu().MilliValue()))

	memoryOvercommitRatio := p.resourceToOvercommitRatio(
		node.Name,
		v1.ResourceMemory.String(),
		float64(nodeResource.Memory().Value()),
		sumPodMemoryTimeSeries.Max().Value,
		float64(nodeAllocatable.Memory().Value()))

	return cpuOvercommitRatio, memoryOvercommitRatio, nil
}

// return CPU timeSeries, memory timeSeries and requestResource
func (p *Prediction) podResourceTimeSeries(pod *v1.Pod) (*common.TimeSeries, *common.TimeSeries, v1.ResourceList) {
	var cpuTs, memoryTs *common.TimeSeries

	// pod request
	podResource := native.SumUpPodRequestResources(pod)

	// pod to workload
	workloadName, workloadType, ok := p.podToWorkloadNameAndType(pod)
	if !ok {
		klog.Warningf("get pod %v-%v workload fail", pod.Namespace, pod.Name)
		cpuTs, memoryTs = p.timeSeriesByRequest(podResource)
		return cpuTs, memoryTs, podResource
	}

	if p.predictor != nil && p.provider != nil {
		// get time series from cache
		cacheKey := workloadUsageCacheName(pod.GetNamespace(), workloadType, workloadName)
		cpuTs = p.getWorkloadUsageCache(cacheKey, v1.ResourceCPU.String())
		memoryTs = p.getWorkloadUsageCache(cacheKey, v1.ResourceMemory.String())
	} else {
		// get time series from spd
		cpuTs, memoryTs = p.getSPDPortrait(pod.GetNamespace(), workloadName)
	}
	if cpuTs == nil || len(cpuTs.Samples) != workloadUsageDataLength {
		cpuTs = p.cpuTimeSeriesByRequest(podResource)
	}
	if memoryTs == nil || len(memoryTs.Samples) != workloadUsageDataLength {
		memoryTs = p.memoryTimeSeriesByRequest(podResource)
	}

	klog.V(6).Infof("workload %v cpu timeseries: %v", workloadName, cpuTs.Samples)
	klog.V(6).Infof("workload %v memory timeseries: %v", workloadName, memoryTs.Samples)
	klog.V(6).Infof("pod %v podResource: %v", pod.Name, podResource.Cpu().MilliValue())

	return cpuTs, memoryTs, podResource
}

func (p *Prediction) getWorkloadUsageCache(key string, resourceName string) *common.TimeSeries {
	p.RLock()
	defer p.RUnlock()

	if p.workloadUsageCache == nil {
		klog.Errorf("getWorkloadUsageCache fail, workloadUsageCache is nil")
		return nil
	}
	cache, ok := p.workloadUsageCache[key]
	if !ok {
		klog.Warningf("%v workloadUsageCache miss", key)
		return nil
	}
	return cache[resourceName]
}

func (p *Prediction) getSPDPortrait(podNamespace string, workloadName string) (*common.TimeSeries, *common.TimeSeries) {
	var (
		cpuTs    = common.EmptyTimeSeries()
		memoryTs = common.EmptyTimeSeries()
	)

	// get spd from lister
	spd, err := p.spdLister.ServiceProfileDescriptors(podNamespace).Get(workloadName)
	if err != nil {
		klog.Errorf("get workload %v spd fail: %v", workloadName, err)
		return nil, nil
	}

	for i := range spd.Status.AggMetrics {
		if spd.Status.AggMetrics[i].Scope != spdPortraitScope {
			continue
		}

		for _, podMetrics := range spd.Status.AggMetrics[i].Items {
			timestamp := podMetrics.Timestamp.Time
			for _, containerMetrics := range podMetrics.Containers {
				if containerMetrics.Name != spdPortraitLoadAwareMetricName {
					continue
				}
				if usage, ok := containerMetrics.Usage[resourceToPortraitMetrics[v1.ResourceCPU.String()]]; ok {
					cpuTs.Samples = append(cpuTs.Samples, common.Sample{
						Timestamp: timestamp.Unix(),
						Value:     float64(usage.MilliValue()),
					})
				}

				if usage, ok := containerMetrics.Usage[resourceToPortraitMetrics[v1.ResourceMemory.String()]]; ok {
					memoryTs.Samples = append(memoryTs.Samples, common.Sample{
						Timestamp: timestamp.Unix(),
						Value:     float64(usage.Value()),
					})
				}
			}
		}
	}

	sort.Sort(common.Samples(cpuTs.Samples))
	sort.Sort(common.Samples(memoryTs.Samples))
	klog.V(6).Infof("getSPDPortrait, cpu: %v, memory: %v", cpuTs.Samples, memoryTs.Samples)
	return cpuTs, memoryTs
}

func (p *Prediction) resourceToOvercommitRatio(nodeName string, resourceName string, request float64, estimateUsage float64, nodeAllocatable float64) float64 {
	if request == 0 {
		klog.Warningf("node %v request is zero", nodeName)
		return 0
	}
	if estimateUsage == 0 {
		klog.Warningf("node %v estimateUsage is zero", nodeName)
		return 0
	}
	if nodeAllocatable == 0 {
		klog.Errorf("node %v allocatable is zero", nodeName)
		return 0
	}

	nodeMaxLoad := estimateUsage / request
	if nodeMaxLoad > 1 {
		nodeMaxLoad = 1
	}
	var podExpectedLoad, nodeTargetLoad float64
	switch resourceName {
	case v1.ResourceCPU.String():
		podExpectedLoad = p.conf.Prediction.PodEstimatedCPULoad
		nodeTargetLoad = p.conf.Prediction.NodeCPUTargetLoad
	case v1.ResourceMemory.String():
		podExpectedLoad = p.conf.Prediction.PodEstimatedMemoryLoad
		nodeTargetLoad = p.conf.Prediction.NodeMemoryTargetLoad
	default:
		klog.Warningf("unknown resourceName: %v", resourceName)
		return 0
	}
	if nodeMaxLoad < podExpectedLoad {
		nodeMaxLoad = podExpectedLoad
	}

	overcommitRatio := ((nodeAllocatable*nodeTargetLoad-estimateUsage)/nodeMaxLoad + request) / nodeAllocatable

	klog.V(6).Infof("resource %v request: %v, allocatable: %v, usage: %v, targetLoad: %v, nodeMaxLoad: %v, overcommitRatio: %v",
		resourceName, request, nodeAllocatable, estimateUsage, nodeTargetLoad, nodeMaxLoad, overcommitRatio)
	if overcommitRatio < 1.0 {
		overcommitRatio = 1.0
	}
	return overcommitRatio
}

func (p *Prediction) podToWorkloadNameAndType(pod *v1.Pod) (string, string, bool) {
	if p.conf.Prediction.TargetReferenceNameKey != "" && p.conf.Prediction.TargetReferenceTypeKey != "" {
		return p.podToWorkloadByLabel(pod)
	}
	return p.podToWorkloadByOwner(pod)
}

// get pod owner name and type by specified label key
func (p *Prediction) podToWorkloadByLabel(pod *v1.Pod) (string, string, bool) {
	if pod.Labels == nil {
		return "", "", false
	}

	workloadName, ok := pod.Labels[p.conf.Prediction.TargetReferenceNameKey]
	if !ok {
		return "", "", false
	}

	workloadType, ok := pod.Labels[p.conf.Prediction.TargetReferenceTypeKey]
	if !ok {
		return "", "", false
	}
	return workloadName, workloadType, ok
}

func (p *Prediction) podToWorkloadByOwner(pod *v1.Pod) (string, string, bool) {
	for _, owner := range pod.OwnerReferences {
		kind := owner.Kind
		switch kind {
		// resource portrait time series predicted and stored by deployment, but pod owned by rs
		case "ReplicaSet":
			names := strings.Split(owner.Name, "-")
			if len(names) <= 1 {
				klog.Warningf("unexpected rs name: %v", owner.Name)
				return "", "", false
			}
			names = names[0 : len(names)-1]
			return strings.Join(names, "-"), "Deployment", true
		default:
			return owner.Name, kind, true
		}
	}

	return "", "", false
}

func (p *Prediction) updatePredictOvercommitRatio(cpu, memory float64, node *v1.Node) error {
	nodeCopy := node.DeepCopy()
	nodeAnnotation := nodeCopy.Annotations
	if nodeAnnotation == nil {
		nodeAnnotation = make(map[string]string)
	}

	if cpu < 1 {
		delete(nodeAnnotation, consts.NodeAnnotationPredictCPUOvercommitRatioKey)
	} else {
		nodeAnnotation[consts.NodeAnnotationPredictCPUOvercommitRatioKey] = fmt.Sprintf("%.2f", cpu)
	}
	if memory < 1 {
		delete(nodeAnnotation, consts.NodeAnnotationPredictMemoryOvercommitRatioKey)
	} else {
		nodeAnnotation[consts.NodeAnnotationPredictMemoryOvercommitRatioKey] = fmt.Sprintf("%.2f", memory)
	}

	nodeCopy.Annotations = nodeAnnotation
	return p.nodeUpdater.PatchNode(p.ctx, node, nodeCopy)
}

// use request * config.scaleFactor as usage time series if pod resource portrait not exist
func (p *Prediction) timeSeriesByRequest(podResource v1.ResourceList) (*common.TimeSeries, *common.TimeSeries) {
	cpuTs := p.cpuTimeSeriesByRequest(podResource)
	memoryTs := p.memoryTimeSeriesByRequest(podResource)
	return cpuTs, memoryTs
}

func (p *Prediction) cpuTimeSeriesByRequest(podResource v1.ResourceList) *common.TimeSeries {
	return p.genTimeSeries(v1.ResourceCPU, podResource, p.conf.Prediction.CPUScaleFactor)
}

func (p *Prediction) memoryTimeSeriesByRequest(podResource v1.ResourceList) *common.TimeSeries {
	return p.genTimeSeries(v1.ResourceMemory, podResource, p.conf.Prediction.MemoryScaleFactor)
}

func (p *Prediction) genTimeSeries(resourceName v1.ResourceName, podResource v1.ResourceList, scaleFactor float64) *common.TimeSeries {
	timeSeries := &common.TimeSeries{
		Samples: make([]common.Sample, 24, 24),
	}

	if quantity, ok := podResource[resourceName]; ok {
		usage := native.MultiplyResourceQuantity(resourceName, quantity, scaleFactor)
		for i := range timeSeries.Samples {
			value := usage.Value()
			if resourceName == v1.ResourceCPU {
				value = usage.MilliValue()
			}
			timeSeries.Samples[i] = common.Sample{
				Value: float64(value),
			}
		}
	}
	return timeSeries
}

func nodePodIndexFunc(obj interface{}) ([]string, error) {
	pod, ok := obj.(*v1.Pod)
	if !ok || pod == nil {
		return nil, fmt.Errorf("failed to reflect a obj to pod")
	}

	if pod.Spec.NodeName == "" {
		return []string{}, nil
	}

	return []string{pod.Spec.NodeName}, nil
}

func (p *Prediction) addDeleteHandler(informers map[string]native.DynamicInformer) {
	if p.predictor != nil && p.provider != nil {
		for _, informer := range informers {
			informer.Informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
				DeleteFunc: p.deleteWorkloadUsageCache,
			})
		}
	}
}

func (p *Prediction) deleteWorkloadUsageCache(obj interface{}) {
	if p.workloadUsageCache == nil {
		return
	}

	object, ok := obj.(*unstructured.Unstructured)
	if !ok {
		klog.Errorf("cannot convert obj to Unstructured: %v", obj)
		return
	}

	name := workloadUsageCacheName(object.GetNamespace(), object.GetKind(), object.GetName())

	p.Lock()
	defer p.Unlock()
	delete(p.workloadUsageCache, name)
}

// get node allocatable before overcommit
func getNodeAllocatable(node *v1.Node) v1.ResourceList {
	res := v1.ResourceList{
		v1.ResourceCPU:    resource.MustParse("0"),
		v1.ResourceMemory: resource.MustParse("0"),
	}

	// get allocatable from node annotation first
	cpu, ok := node.Annotations[consts.NodeAnnotationOriginalAllocatableCPUKey]
	if ok {
		res[v1.ResourceCPU] = resource.MustParse(cpu)
	} else {
		// if no allocatable in node annotation, get allocatable from node resource
		res[v1.ResourceCPU] = *node.Status.Allocatable.Cpu()
	}

	mem, ok := node.Annotations[consts.NodeAnnotationOriginalAllocatableMemoryKey]
	if ok {
		res[v1.ResourceMemory] = resource.MustParse(mem)
	} else {
		res[v1.ResourceMemory] = *node.Status.Allocatable.Memory()
	}
	return res
}
