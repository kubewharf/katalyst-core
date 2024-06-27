package loadaware

import (
	"context"
	"fmt"
	"sort"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	toolscache "k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config"
	"github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config/validation"
	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	listers "github.com/kubewharf/katalyst-api/pkg/client/listers/node/v1alpha1"
	workloadlisters "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	Name                 = "LoadAware"
	loadAwareMetricScope = "loadAware"

	DefaultMilliCPURequest int64 = 250               // 0.25 core
	DefaultMemoryRequest   int64 = 200 * 1024 * 1024 // 200 MB

	portraitItemsLength            = 24
	spdPortraitLoadAwareMetricName = "overcommit-predict"
	spdPortraitScope               = "ResourcePortraitIndicatorPlugin"

	cpuUsageMetric    = "cpu_utilization_usage_seconds_max"
	memoryUsageMetric = "memory_utilization_max"
)

var (
	_ framework.FilterPlugin  = &Plugin{}
	_ framework.ScorePlugin   = &Plugin{}
	_ framework.ReservePlugin = &Plugin{}
)

type Plugin struct {
	handle       framework.Handle
	args         *config.LoadAwareArgs
	npdLister    listers.NodeProfileDescriptorLister
	spdLister    workloadlisters.ServiceProfileDescriptorLister
	spdHasSynced toolscache.InformerSynced
}

func NewPlugin(args runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	klog.Infof("new loadAware scheduler plugin")
	pluginArgs, ok := args.(*config.LoadAwareArgs)
	if !ok {
		return nil, fmt.Errorf("want args to be of type LoadAwareArgs, got %T", args)
	}
	if err := validation.ValidateLoadAwareSchedulingArgs(pluginArgs); err != nil {
		klog.Errorf("validate pluginArgs fail, err: %v", err)
		return nil, err
	}

	p := &Plugin{
		handle: handle,
		args:   pluginArgs,
	}
	p.registerNPDHandler()
	p.registerSPDHandler()
	RegisterPodHandler()

	if p.enablePortrait() {
		cache.SetSPDLister(p)
	}

	go func() {
		wait.Until(func() {
			if p.spdHasSynced == nil || !p.spdHasSynced() {
				klog.Warningf("portrait has not synced, skip")
				return
			}
			cache.ReconcilePredictUsage()
		}, time.Hour, context.TODO().Done())
	}()

	return p, nil
}

func (p *Plugin) Name() string {
	return Name
}

func (p *Plugin) GetPodPortrait(pod *v1.Pod) *ResourceUsage {
	startTime := time.Now()
	defer func() {
		if klog.V(6).Enabled() {
			duration := time.Now().Sub(startTime)
			klog.Infof("GetPodPortrait duration: %v", duration.String())
		}
	}()
	ownerName, _, ok := podToWorkloadByOwner(pod)
	if !ok {
		return p.portraitByRequest(pod)
	}

	// get pod workload spd
	spd, err := p.getSPD(ownerName, pod.GetNamespace())
	if err != nil {
		return p.portraitByRequest(pod)
	}

	// get portrait metrics from spd
	podResourceUsage, err := p.getPortraitTimeSeries(spd)
	if err != nil {
		klog.Errorf("getPortraitTimeSeries fail, namespace: %v, workload: %v, err: %v", pod.GetNamespace(), ownerName, err)
		return p.portraitByRequest(pod)
	}

	// validate metrics
	if len(podResourceUsage.Cpu) != portraitItemsLength {
		resourceList := native.SumUpPodRequestResources(pod)
		cpuScaleFactor := p.args.ResourceToScalingFactorMap[v1.ResourceCPU]
		if cpuScaleFactor == 0 {
			cpuScaleFactor = 100
		}
		podResourceUsage.Cpu = cpuTimeSeriesByRequest(resourceList, float64(cpuScaleFactor)/100.0)
	}
	if len(podResourceUsage.Memory) != portraitItemsLength {
		resourceList := native.SumUpPodRequestResources(pod)
		memScaleFactor := p.args.ResourceToScalingFactorMap[v1.ResourceMemory]
		if memScaleFactor == 0 {
			memScaleFactor = 100
		}
		podResourceUsage.Memory = memoryTimeSeriesByRequest(resourceList, float64(memScaleFactor)/100.0)
	}

	return podResourceUsage
}

func (p *Plugin) getSPD(workloadName, namespace string) (*v1alpha1.ServiceProfileDescriptor, error) {
	spd, err := p.spdLister.ServiceProfileDescriptors(namespace).Get(workloadName)
	if err != nil {
		klog.V(5).Infof("get SPD fail, workloadName: %v, namespace: %v, err: %v", workloadName, namespace, err)
		return nil, err
	}
	if spd == nil {
		err = fmt.Errorf("get nil SPD, workloadName: %v, namespace: %v", workloadName, namespace)
		klog.V(5).Infof(err.Error())
		return nil, err
	}

	return spd, nil
}

func (p *Plugin) getPortraitTimeSeries(spd *v1alpha1.ServiceProfileDescriptor) (*ResourceUsage, error) {
	if spd == nil {
		return nil, fmt.Errorf("spd is nil")
	}

	var (
		cpuUsages    = make([]Item, 0)
		memoryUsages = make([]Item, 0)
		res          = &ResourceUsage{
			Cpu:    make([]float64, 0),
			Memory: make([]float64, 0),
		}
	)

	for i := range spd.Status.AggMetrics {
		if spd.Status.AggMetrics[i].Scope != spdPortraitScope {
			continue
		}

		// podMetric contains metrics from multiple sources at a certain timestamp
		for j := range spd.Status.AggMetrics[i].Items {
			t := spd.Status.AggMetrics[i].Items[j].Timestamp.Time
			for _, metrics := range spd.Status.AggMetrics[i].Items[j].Containers {
				if metrics.Name == spdPortraitLoadAwareMetricName {
					cpuUsage, ok := metrics.Usage[cpuUsageMetric]
					if ok {
						cpuUsages = append(cpuUsages, Item{
							Value:     cpuUsage.MilliValue(),
							Timestamp: t,
						})
					}

					memoryUsage, ok := metrics.Usage[memoryUsageMetric]
					if ok {
						memoryUsages = append(memoryUsages, Item{
							Value:     memoryUsage.Value(),
							Timestamp: t,
						})
					}
				}
			}
		}
	}

	if len(cpuUsages) != portraitItemsLength {
		klog.Errorf("portrait %v metric more than %v: %v", cpuUsageMetric, portraitItemsLength, len(cpuUsages))
		cpuUsages = make([]Item, 0)
	}
	if len(memoryUsages) != portraitItemsLength {
		klog.Errorf("portrait %v metric more than %v: %v", memoryUsageMetric, portraitItemsLength, len(memoryUsages))
		memoryUsages = make([]Item, 0)
	}

	sort.Sort(Items(cpuUsages))
	sort.Sort(Items(memoryUsages))
	for i := range cpuUsages {
		res.Cpu = append(res.Cpu, float64(cpuUsages[i].Value))
	}
	for i := range memoryUsages {
		res.Memory = append(res.Memory, float64(memoryUsages[i].Value))
	}

	return res, nil
}

func (p *Plugin) portraitByRequest(pod *v1.Pod) *ResourceUsage {
	res := &ResourceUsage{}

	resourceList := native.SumUpPodRequestResources(pod)

	cpuScaleFactor := p.args.ResourceToScalingFactorMap[v1.ResourceCPU]
	if cpuScaleFactor == 0 {
		cpuScaleFactor = 100
	}
	memScaleFactor := p.args.ResourceToScalingFactorMap[v1.ResourceMemory]
	if memScaleFactor == 0 {
		memScaleFactor = 100
	}
	cpuSeries := cpuTimeSeriesByRequest(resourceList, float64(cpuScaleFactor)/100.0)
	memSeries := memoryTimeSeriesByRequest(resourceList, float64(memScaleFactor)/100.0)

	res.Cpu = cpuSeries
	res.Memory = memSeries
	return res
}

func (p *Plugin) getNodePredictUsage(pod *v1.Pod, nodeName string) (*ResourceUsage, error) {
	nodePredictUsage := cache.GetNodePredictUsage(nodeName)
	klog.V(6).Infof("node %v predict usage cpu: %v, memory: %v", nodeName, nodePredictUsage.Cpu, nodePredictUsage.Memory)

	podPredictUsage := p.GetPodPortrait(pod)
	klog.V(6).Infof("pod %v predict usage cpu: %v, memory: %v", pod.Name, podPredictUsage.Cpu, podPredictUsage.Memory)

	err := nodePredictUsage.add(podPredictUsage)
	if err != nil {
		err = fmt.Errorf("sum node %s predict usage fail: %v", nodeName, err)
		return nil, err
	}

	return nodePredictUsage, nil
}

func (p *Plugin) IsLoadAwareEnabled(pod *v1.Pod) bool {
	if p.args.PodAnnotationLoadAwareEnable == nil || *p.args.PodAnnotationLoadAwareEnable == "" {
		return true
	}

	if flag, ok := pod.Annotations[*p.args.PodAnnotationLoadAwareEnable]; ok && flag == consts.PodAnnotationLoadAwareEnableTrue {
		return true
	}
	return false
}

func (p *Plugin) enablePortrait() bool {
	if p.args.EnablePortrait == nil {
		return false
	}
	return *p.args.EnablePortrait
}
