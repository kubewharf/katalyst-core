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

package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	nodeapis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	schedutil "github.com/kubewharf/katalyst-core/pkg/scheduler/util"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	nodeMetricsReporterPluginName = "node-metrics-reporter-plugin"

	Guaranteed PodResourceType = "guaranteed"
	BestEffort PodResourceType = "best-effort"
	Unknown    PodResourceType = "unknown"
)

type PodResourceType string

type nodeMetricsReporterImpl struct {
	skeleton.GenericPlugin
}

type metricAggregators map[string]general.SmoothWindow

func (m metricAggregators) gc() {
	for name, window := range m {
		if window.Empty() {
			delete(m, name)
		}
	}
}

// NewNodeMetricsReporter returns a wrapper of node metrics reporter
func NewNodeMetricsReporter(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	metaReader metacache.MetaReader, conf *config.Configuration,
) (Reporter, error) {
	plugin, err := newNodeMetricsReporterPlugin(emitter, metaServer, metaReader, conf)
	if err != nil {
		return nil, fmt.Errorf("[node-metrics-reporter] failed to create reporter, %v", err)
	}

	return &nodeMetricsReporterImpl{plugin}, nil
}

func (r *nodeMetricsReporterImpl) Run(ctx context.Context) {
	if err := r.Start(); err != nil {
		klog.Fatalf("[node-metrics-reporter] failed to start %v", err)
	}
	klog.Infof("[node-metrics-reporter] plugin wrapper %s started", r.Name())

	<-ctx.Done()
	if err := r.Stop(); err != nil {
		klog.Errorf("[node-metrics-reporter] stop %v failed: %v", r.Name(), err)
	}
}

type nodeMetricsReporterPlugin struct {
	sync.RWMutex
	started bool

	metaServer *metaserver.MetaServer
	metaReader metacache.MetaReader
	emitter    metrics.MetricEmitter
	qosConf    *generic.QoSConfiguration

	stop              chan struct{}
	syncPeriod        time.Duration
	metricAggregators metricAggregators
	smoothWindowOpts  map[v1.ResourceName]general.SmoothWindowOpts

	nodeMetricStatus *nodeapis.NodeMetricStatus
}

func newNodeMetricsReporterPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer, metaReader metacache.MetaReader, conf *config.Configuration) (skeleton.GenericPlugin, error) {
	reporter := &nodeMetricsReporterPlugin{
		RWMutex:           sync.RWMutex{},
		metaServer:        metaServer,
		metaReader:        metaReader,
		emitter:           emitter,
		qosConf:           conf.QoSConfiguration,
		syncPeriod:        conf.NodeMetricReporterConfiguration.SyncPeriod,
		metricAggregators: map[string]general.SmoothWindow{},
		smoothWindowOpts: map[v1.ResourceName]general.SmoothWindowOpts{
			v1.ResourceCPU: {
				WindowSize:    int(conf.MetricSlidingWindowTime.Seconds() / conf.NodeMetricReporterConfiguration.SyncPeriod.Seconds()),
				TTL:           conf.MetricSlidingWindowTime * 2,
				UsedMillValue: true,
				AggregateFunc: conf.AggregateFuncs[v1.ResourceCPU],
				AggregateArgs: conf.AggregateArgs[v1.ResourceCPU],
			},
			v1.ResourceMemory: {
				WindowSize:    int(conf.MetricSlidingWindowTime.Seconds() / conf.NodeMetricReporterConfiguration.SyncPeriod.Seconds()),
				TTL:           conf.MetricSlidingWindowTime * 2,
				UsedMillValue: false,
				AggregateFunc: conf.AggregateFuncs[v1.ResourceMemory],
				AggregateArgs: conf.AggregateArgs[v1.ResourceMemory],
			},
		},
	}
	return skeleton.NewRegistrationPluginWrapper(reporter, []string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"pluginName": nodeMetricsReporterPluginName,
				"pluginType": registration.ReporterPlugin,
			})...)
		})
}

func (p *nodeMetricsReporterPlugin) Name() string {
	return nodeMetricsReporterPluginName
}

func (p *nodeMetricsReporterPlugin) Start() (err error) {
	p.Lock()
	defer func() {
		if err == nil {
			p.started = true
		}
		p.Unlock()
	}()

	if p.started {
		return
	}

	p.stop = make(chan struct{})
	general.RegisterHeartbeatCheck(nodeMetricsReporterPluginName, healthCheckTolerationDuration, general.HealthzCheckStateNotReady, healthCheckTolerationDuration)
	go wait.Until(p.updateNodeMetrics, p.syncPeriod, p.stop)
	return
}

func (p *nodeMetricsReporterPlugin) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()
	}()

	// plugin.Stop may be called before plugin.Start or multiple times,
	// we should ensure cancel function exist
	if !p.started {
		return nil
	}

	if p.stop != nil {
		close(p.stop)
	}
	return nil
}

func (p *nodeMetricsReporterPlugin) GetReportContent(_ context.Context, _ *v1alpha1.Empty) (*v1alpha1.GetReportContentResponse, error) {
	general.InfoS("called")
	reportToCNR, err := p.getReportNodeMetricsForCNR()
	defer func() {
		_ = general.UpdateHealthzStateByError(nodeMetricsReporterPluginName, err)
	}()
	if err != nil {
		return nil, err
	}

	return &v1alpha1.GetReportContentResponse{
		Content: []*v1alpha1.ReportContent{
			reportToCNR,
		},
	}, nil
}

func (p *nodeMetricsReporterPlugin) getReportNodeMetricsForCNR() (*v1alpha1.ReportContent, error) {
	p.RWMutex.RLock()
	defer p.RWMutex.RUnlock()

	if p.nodeMetricStatus == nil {
		return nil, fmt.Errorf("get nodeMetricStatus failed")
	}

	nodeMetricsValue, err := json.Marshal(p.nodeMetricStatus)
	if err != nil {
		return nil, err
	}

	general.Infof("nodeMetricsValue %v", string(nodeMetricsValue))

	return &v1alpha1.ReportContent{
		GroupVersionKind: &util.CNRGroupVersionKind,
		Field: []*v1alpha1.ReportField{
			{
				FieldType: v1alpha1.FieldType_Status,
				FieldName: util.CNRFieldNameNodeMetricStatus,
				Value:     nodeMetricsValue,
			},
		},
	}, nil
}

func (p *nodeMetricsReporterPlugin) ListAndWatchReportContent(_ *v1alpha1.Empty, server v1alpha1.ReporterPlugin_ListAndWatchReportContentServer) error {
	for {
		select {
		case <-server.Context().Done():
			return nil
		case <-p.stop:
			return nil
		}
	}
}

func (p *nodeMetricsReporterPlugin) updateNodeMetrics() {
	general.InfoS("try to update node metrics")

	var errList []error
	nodeMetricInfo, err := p.getNodeMetricInfo()
	if err != nil {
		errList = append(errList, err)
	}

	groupMetricInfo, err := p.getGroupMetricInfo()
	if err != nil {
		errList = append(errList, err)
	}

	err = errors.NewAggregate(errList)
	if err != nil {
		general.ErrorS(err, "updateNodeMetrics failed")
		return
	}

	nms := nodeapis.NodeMetricStatus{
		UpdateTime:  metav1.NewTime(time.Now()),
		NodeMetric:  nodeMetricInfo,
		GroupMetric: groupMetricInfo,
	}

	p.RWMutex.Lock()
	defer p.RWMutex.Unlock()
	p.nodeMetricStatus = &nms
}

func (p *nodeMetricsReporterPlugin) getNodeMetricInfo() (*nodeapis.NodeMetricInfo, error) {
	var errList []error

	nmi := &nodeapis.NodeMetricInfo{
		ResourceUsage: nodeapis.ResourceUsage{
			NUMAUsage:    make([]nodeapis.NUMAMetricInfo, 0),
			GenericUsage: &nodeapis.ResourceMetric{},
		},
	}

	memory, err := p.getNodeMemoryUsage()
	if err != nil {
		errList = append(errList, err)
	} else {
		nmi.GenericUsage.Memory = memory
	}

	cpuUsage, err := p.getNodeCPUUsage()
	if err != nil {
		errList = append(errList, err)
	} else {
		nmi.GenericUsage.CPU = cpuUsage
	}

	for numaID := 0; numaID < p.metaServer.NumNUMANodes; numaID++ {
		numaUsage := nodeapis.NUMAMetricInfo{NUMAId: numaID, Usage: &nodeapis.ResourceMetric{}}
		memoryUsage, err := p.getNodeNUMAMemoryUsage(numaID)
		if err != nil {
			errList = append(errList, err)
		} else {
			numaUsage.Usage.Memory = memoryUsage
		}

		numaCpuUsage, err := p.getNodeNUMACPUUsage(numaID)
		if err != nil {
			errList = append(errList, err)
		} else {
			numaUsage.Usage.CPU = numaCpuUsage
		}

		nmi.NUMAUsage = append(nmi.NUMAUsage, numaUsage)
	}
	return nmi, errors.NewAggregate(errList)
}

func newGroupMetricInfo(qosLevel string) *nodeapis.GroupMetricInfo {
	return &nodeapis.GroupMetricInfo{
		QoSLevel: qosLevel,
		ResourceUsage: nodeapis.ResourceUsage{
			NUMAUsage:    make([]nodeapis.NUMAMetricInfo, 0),
			GenericUsage: &nodeapis.ResourceMetric{},
		},
		PodList: make([]string, 0),
	}
}

func (p *nodeMetricsReporterPlugin) getGroupMetricInfo() ([]nodeapis.GroupMetricInfo, error) {
	var errList []error

	pods, err := p.metaServer.GetPodList(context.TODO(), native.PodIsActive)
	if err != nil {
		return nil, fmt.Errorf("failed to get pod list")
	}

	qosLevel2Pods := make(map[string][]*v1.Pod)
	for _, pod := range pods {
		qosLevel, err := p.qosConf.GetQoSLevelForPod(pod)
		if err != nil {
			general.ErrorS(err, "failed to GetQoSLevelForPod", "pod", pod.Name)
			continue
		}
		qosLevel2Pods[qosLevel] = append(qosLevel2Pods[qosLevel], pod)
	}

	qosLevels := []string{
		apiconsts.PodAnnotationQoSLevelDedicatedCores,
		apiconsts.PodAnnotationQoSLevelSharedCores,
		apiconsts.PodAnnotationQoSLevelReclaimedCores,
		apiconsts.PodAnnotationQoSLevelSystemCores,
	}

	groupMetrics := make([]nodeapis.GroupMetricInfo, 0)

	for _, qosLevel := range qosLevels {
		metricInfo := *newGroupMetricInfo(qosLevel)
		pods, ok := qosLevel2Pods[qosLevel]
		if !ok || len(pods) == 0 {
			continue
		}
		groupUsage, groupNUMAUsages, effectivePods, err := p.getGroupUsage(pods, qosLevel)
		if err != nil {
			errList = append(errList, err)
		} else {
			metricInfo.GenericUsage = groupUsage
			metricInfo.NUMAUsage = groupNUMAUsages
			for _, pod := range effectivePods {
				metricInfo.PodList = append(metricInfo.PodList, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}.String())
			}
		}
		groupMetrics = append(groupMetrics, metricInfo)
	}

	return groupMetrics, errors.NewAggregate(errList)
}

func (p *nodeMetricsReporterPlugin) getPodUsage(pod *v1.Pod) (v1.ResourceList, map[int]v1.ResourceList, machine.CPUSet, bool, error) {
	rampUp := false
	assignedNUMAs := machine.NewCPUSet()
	numaUsage := make(map[int]v1.ResourceList)

	var errList []error
	containers, ok := p.metaReader.GetContainerEntries(string(pod.UID))
	if !ok {
		return nil, nil, assignedNUMAs, false, fmt.Errorf("failed to get container info for pod %v", pod.Name)
	}
	podCPUUsage := .0
	podMemUsage := .0
	for _, container := range containers {
		if container.RampUp {
			rampUp = true
		}

		containerInfo, existed := p.metaReader.GetContainerInfo(string(pod.UID), container.ContainerName)
		if !existed {
			return nil, nil, assignedNUMAs, false, fmt.Errorf("failed to get container info for %v/%v", pod.Name, container.ContainerName)
		}

		assignedNUMAs.Add(machine.GetCPUAssignmentNUMAs(containerInfo.TopologyAwareAssignments).ToSliceInt()...)

		metricContainerCPUUsage, err := p.metaServer.GetContainerMetric(string(pod.UID), container.ContainerName, consts.MetricCPUUsageContainer)
		if err != nil {
			errList = append(errList, fmt.Errorf("failed to get MetricCPUUsageContainer, podUID=%v, containerName=%v, err=%v", pod.UID, container.ContainerName, err))
		} else {
			podCPUUsage += metricContainerCPUUsage.Value
		}

		metricContainerMemUsage, err := p.metaServer.GetContainerMetric(string(pod.UID), container.ContainerName, consts.MetricMemUsageContainer)
		if err != nil {
			errList = append(errList, fmt.Errorf("failed to get MetricMemUsageContainer, podUID=%v, containerName=%v, err=%v", pod.UID, container.ContainerName, err))
		}
		metricContainerMemCache, err := p.metaServer.GetContainerMetric(string(pod.UID), container.ContainerName, consts.MetricMemCacheContainer)
		if err != nil {
			errList = append(errList, fmt.Errorf("failed to get MetricMemCacheContainer, podUID=%v, containerName=%v, err=%v", pod.UID, container.ContainerName, err))
		}
		podMemUsage += metricContainerMemUsage.Value - metricContainerMemCache.Value

		metricContainerNUMACPUUsage, err := p.metaServer.GetContainerNumaMetrics(string(pod.UID), container.ContainerName, consts.MetricsCPUUsageNUMAContainer)
		if err != nil {
			errList = append(errList, fmt.Errorf("failed to get MetricsCPUUsageNUMAContainer, podUID=%v, containerName=%v, err=%v", pod.UID, container.ContainerName, err))
		}
		for numaID, metricCPUUsage := range metricContainerNUMACPUUsage {
			usages, ok := numaUsage[numaID]
			if !ok {
				usages = make(v1.ResourceList)
			}

			cpuUsage, ok := usages[v1.ResourceCPU]
			if !ok {
				cpuUsage = *resource.NewMilliQuantity(0, resource.DecimalSI)
			}
			cpuUsage.Add(*resource.NewMilliQuantity(int64(metricCPUUsage.Value*1000), resource.DecimalSI))
			usages[v1.ResourceCPU] = cpuUsage
			numaUsage[numaID] = usages
		}

		metricContainerAnonMemoryUsage, err := p.metaServer.GetContainerNumaMetrics(string(pod.UID), container.ContainerName, consts.MetricsMemAnonPerNumaContainer)
		if err != nil {
			errList = append(errList, fmt.Errorf("failed to get MetricsMemAnonPerNumaContainer, podUID=%v, containerName=%v, err=%v", pod.UID, container.ContainerName, err))
		}
		for numaID, metricAnonMemoryUsage := range metricContainerAnonMemoryUsage {
			usages, ok := numaUsage[numaID]
			if !ok {
				usages = make(v1.ResourceList)
			}

			memUsage, ok := usages[v1.ResourceMemory]
			if !ok {
				memUsage = *resource.NewQuantity(0, resource.BinarySI)
			}
			memUsage.Add(*resource.NewQuantity(int64(metricAnonMemoryUsage.Value), resource.BinarySI))
			usages[v1.ResourceMemory] = memUsage
			numaUsage[numaID] = usages
		}
	}

	cpu := resource.NewMilliQuantity(int64(podCPUUsage*1000), resource.DecimalSI)
	memory := resource.NewQuantity(int64(podMemUsage), resource.BinarySI)

	return v1.ResourceList{v1.ResourceMemory: *memory, v1.ResourceCPU: *cpu}, numaUsage, assignedNUMAs, rampUp, errors.NewAggregate(errList)
}

func (p *nodeMetricsReporterPlugin) getGroupUsage(pods []*v1.Pod, qosLevel string) (*nodeapis.ResourceMetric, []nodeapis.NUMAMetricInfo, []*v1.Pod, error) {
	var errList []error

	cpu := resource.NewQuantity(0, resource.DecimalSI)
	memory := resource.NewQuantity(0, resource.BinarySI)

	numaUsages := make(map[int]v1.ResourceList)

	effectivePods := make([]*v1.Pod, 0)
	for _, pod := range pods {
		podUsage, podNUMAUsage, assignedNUMAs, rampUp, err := p.getPodUsage(pod)
		if err != nil {
			general.ErrorS(err, "failed to getPodUsage", "pod", pod.Name)
			continue
		}
		effectivePods = append(effectivePods, pod)
		if rampUp {
			req := schedutil.GetPodEffectiveRequest(pod)
			if podUsage.Cpu().Cmp(*req.Cpu()) < 0 {
				podUsage[v1.ResourceCPU] = *req.Cpu()
			}
			if podUsage.Memory().Cmp(*req.Memory()) < 0 {
				podUsage[v1.ResourceMemory] = *req.Memory()
			}

			for numaID := range assignedNUMAs.ToSliceInt() {
				podNUMAUsage[numaID] = map[v1.ResourceName]resource.Quantity{
					v1.ResourceCPU:    *resource.NewMilliQuantity(req.Cpu().MilliValue()/int64(assignedNUMAs.Size()), resource.DecimalSI),
					v1.ResourceMemory: *resource.NewQuantity(req.Memory().Value()/int64(assignedNUMAs.Size()), resource.BinarySI),
				}
			}
		}
		cpu.Add(*podUsage.Cpu())
		memory.Add(*podUsage.Memory())

		for numaID, podUsages := range podNUMAUsage {
			usages, ok := numaUsages[numaID]
			if !ok {
				usages = make(v1.ResourceList)
			}
			for name, podResourceUsage := range podUsages {
				usage, ok := usages[name]
				if !ok {
					usage = *resource.NewMilliQuantity(0, resource.DecimalSI)
				}
				usage.Add(podResourceUsage)
				usages[name] = usage
			}
			numaUsages[numaID] = usages
		}

		klog.InfoS("pod usage", "pod", pod.Name, "cpu", podUsage.Cpu().AsApproximateFloat64(),
			"memory", general.FormatMemoryQuantity(podUsage.Memory().AsApproximateFloat64()), "numaUsage", numaUsages)
	}

	resourceMetric := &nodeapis.ResourceMetric{}
	resourceNUMAMetrics := make([]nodeapis.NUMAMetricInfo, 0)
	aggMemory := p.getAggregatedMetric(memory, v1.ResourceMemory, "getGroupUsage", qosLevel, "memory")
	if aggMemory == nil {
		errList = append(errList, fmt.Errorf("failed to get enhough samples for group memory, qosLevel=%v", qosLevel))
	} else {
		resourceMetric.Memory = aggMemory
	}

	aggCPU := p.getAggregatedMetric(cpu, v1.ResourceCPU, "getGroupUsage", qosLevel, "cpu")
	if aggCPU == nil {
		errList = append(errList, fmt.Errorf("failed to get enhough samples for group cpu, qosLevel=%v", qosLevel))
	} else {
		resourceMetric.CPU = aggCPU
	}

	for numaID := 0; numaID < p.metaServer.NumNUMANodes; numaID++ {
		resourceUsages, ok := numaUsages[numaID]
		if !ok {
			continue
		}
		resourceNUMAMetric := nodeapis.ResourceMetric{}

		cpuUsage := resourceUsages[v1.ResourceCPU]
		aggNUMACPU := p.getAggregatedMetric(&cpuUsage, v1.ResourceCPU, "getGroupNUMAUsage", qosLevel, "cpu", strconv.Itoa(numaID))
		if aggNUMACPU == nil {
			errList = append(errList, fmt.Errorf("failed to get enhough samples for group numa cpu, qosLevel=%v, numa=%v", qosLevel, numaID))
		} else {
			resourceNUMAMetric.CPU = aggNUMACPU
		}

		memUsage := resourceUsages[v1.ResourceMemory]
		aggNUMAMem := p.getAggregatedMetric(&memUsage, v1.ResourceMemory, "getGroupNUMAUsage", qosLevel, "memory", strconv.Itoa(numaID))
		if aggNUMAMem == nil {
			errList = append(errList, fmt.Errorf("failed to get enhough samples for group numa memory, qosLevel=%v, numa=%v", qosLevel, numaID))
		} else {
			resourceNUMAMetric.Memory = aggNUMAMem
		}

		resourceNUMAMetrics = append(resourceNUMAMetrics, nodeapis.NUMAMetricInfo{
			NUMAId: numaID,
			Usage:  &resourceNUMAMetric,
		})
	}

	err := errors.NewAggregate(errList)
	if err != nil {
		return nil, nil, nil, err
	}

	klog.InfoS("group usage", "qosLevel", qosLevel, "memory", general.FormatMemoryQuantity(memory.AsApproximateFloat64()),
		"aggMemory", general.FormatMemoryQuantity(aggMemory.AsApproximateFloat64()), "cpu", cpu.AsApproximateFloat64(), "aggCPU", aggCPU.AsApproximateFloat64())

	return resourceMetric, resourceNUMAMetrics, effectivePods, nil
}

func (p *nodeMetricsReporterPlugin) getNodeMemoryUsage() (*resource.Quantity, error) {
	metricMemUsed, err := p.metaServer.GetNodeMetric(consts.MetricMemUsedSystem)
	if err != nil {
		return nil, fmt.Errorf("failed to get MetricMemUsedSystem, err %v", err)
	}

	v := resource.NewQuantity(int64(metricMemUsed.Value), resource.BinarySI)
	v = p.getAggregatedMetric(v, v1.ResourceMemory, "getNodeMemoryUsage")
	if v == nil {
		return nil, fmt.Errorf("failed to get enough samples for node memory")
	}
	return v, nil
}

func (p *nodeMetricsReporterPlugin) getNodeCPUUsage() (*resource.Quantity, error) {
	metricCPUUsageRatio, err := p.metaServer.GetNodeMetric(consts.MetricCPUUsageRatio)
	if err != nil {
		return nil, fmt.Errorf("failed to get MetricCPUUsageRatio, err %v", err)
	}
	v := resource.NewMilliQuantity(int64(metricCPUUsageRatio.Value*float64(p.metaServer.CPUTopology.NumCPUs)*1000), resource.DecimalSI)
	v = p.getAggregatedMetric(v, v1.ResourceCPU, "getNodeCPUUsage")
	if v == nil {
		return nil, fmt.Errorf("failed to get enough samples for node cpu")
	}
	return v, nil
}

func (p *nodeMetricsReporterPlugin) getNodeNUMAMemoryUsage(numaID int) (*resource.Quantity, error) {
	metricNumaMemUsed, err := p.metaServer.GetNumaMetric(numaID, consts.MetricMemUsedNuma)
	if err != nil {
		return nil, fmt.Errorf("failed to get MetricMemUsedNuma of numa%v, err %v", numaID, err)
	}
	metricNumaMemFile, err := p.metaServer.GetNumaMetric(numaID, consts.MetricMemFilepageNuma)
	if err != nil {
		return nil, fmt.Errorf("failed to get MetricMemFilepageNuma of numa%v, err %v", numaID, err)
	}
	v := resource.NewQuantity(int64(metricNumaMemUsed.Value-metricNumaMemFile.Value), resource.BinarySI)
	v = p.getAggregatedMetric(v, v1.ResourceMemory, "getNodeNUMAMemoryUsage", strconv.Itoa(numaID))
	if v == nil {
		return nil, fmt.Errorf("failed to get enough samples for numa%v memory", numaID)
	}
	return v, nil
}

func (p *nodeMetricsReporterPlugin) getNodeNUMACPUUsage(numaID int) (*resource.Quantity, error) {
	metricCPUUsageNuma, err := p.metaServer.GetNumaMetric(numaID, consts.MetricCPUUsageNuma)
	if err != nil {
		return nil, fmt.Errorf("failed to get metricCPUUsageNuma of numa%v, err %v", numaID, err)
	}
	v := resource.NewMilliQuantity(int64(metricCPUUsageNuma.Value*1000), resource.DecimalSI)
	v = p.getAggregatedMetric(v, v1.ResourceCPU, "getNodeNUMACPUUsage", strconv.Itoa(numaID))
	if v == nil {
		return nil, fmt.Errorf("failed to get enough samples for numa%v memory", numaID)
	}
	return v, nil
}

func (p *nodeMetricsReporterPlugin) getAggregatedMetric(value *resource.Quantity, resourceName v1.ResourceName, funcName string, args ...string) *resource.Quantity {
	opts, ok := p.smoothWindowOpts[resourceName]
	if !ok {
		general.Warningf("failed to find smoothWindowOpts for %v", resourceName)
		return value
	}
	uniqName := funcName
	for _, arg := range args {
		uniqName += arg
	}
	aggregator, ok := p.metricAggregators[uniqName]
	if !ok {
		aggregator = general.NewAggregatorSmoothWindow(opts)
		p.metricAggregators[uniqName] = aggregator
	}
	return aggregator.GetWindowedResources(*value)
}
