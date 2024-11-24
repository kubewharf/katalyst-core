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

package resource

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricsNameHeadroomReportResult = "headroom_report_result"
)

type GetGenericReclaimOptionsFunc func() GenericReclaimOptions

type GenericReclaimOptions struct {
	// EnableReclaim whether enable reclaim resource
	EnableReclaim bool
	// ReservedResourceForReport reserved resource for reporting to cnr
	ReservedResourceForReport resource.Quantity
	// MinReclaimedResourceForReport min reclaimed resource for reporting to cnr
	MinReclaimedResourceForReport resource.Quantity
}

type GenericSlidingWindowOptions struct {
	// SlidingWindowTime duration of sliding window
	SlidingWindowTime time.Duration
	// MinStep min step of the value change
	MinStep resource.Quantity
	// MaxStep max step of the value change
	MaxStep       resource.Quantity
	AggregateFunc string
	AggregateArgs string
}

type GenericHeadroomManager struct {
	sync.RWMutex
	lastReportResult *resource.Quantity
	// the latest transformed reporter result per numa
	lastNUMAReportResult map[int]resource.Quantity

	metaServer              *metaserver.MetaServer
	headroomAdvisor         hmadvisor.ResourceAdvisor
	emitter                 metrics.MetricEmitter
	useMilliValue           bool
	slidingWindowOptions    GenericSlidingWindowOptions
	reportSlidingWindow     general.SmoothWindow
	reportNUMASlidingWindow map[int]general.SmoothWindow

	reportResultTransformer func(quantity resource.Quantity) resource.Quantity
	resourceName            v1.ResourceName
	syncPeriod              time.Duration
	getReclaimOptions       GetGenericReclaimOptionsFunc
}

func NewGenericHeadroomManager(name v1.ResourceName, useMilliValue, reportMilliValue bool,
	syncPeriod time.Duration, headroomAdvisor hmadvisor.ResourceAdvisor,
	emitter metrics.MetricEmitter, slidingWindowOptions GenericSlidingWindowOptions,
	getReclaimOptions GetGenericReclaimOptionsFunc,
	metaServer *metaserver.MetaServer,
) *GenericHeadroomManager {
	// Sliding window size and ttl are calculated by SlidingWindowTime and syncPeriod,
	// the valid lifetime of all samples is twice the duration of the sliding window.
	slidingWindowSize := int(slidingWindowOptions.SlidingWindowTime / syncPeriod)
	slidingWindowTTL := slidingWindowOptions.SlidingWindowTime * 2

	reportResultTransformer := func(quantity resource.Quantity) resource.Quantity {
		if reportMilliValue {
			return *resource.NewQuantity(quantity.MilliValue(), quantity.Format)
		}
		return quantity
	}

	return &GenericHeadroomManager{
		resourceName:            name,
		lastNUMAReportResult:    make(map[int]resource.Quantity),
		reportResultTransformer: reportResultTransformer,
		syncPeriod:              syncPeriod,
		headroomAdvisor:         headroomAdvisor,
		useMilliValue:           useMilliValue,
		slidingWindowOptions:    slidingWindowOptions,
		reportSlidingWindow: general.NewCappedSmoothWindow(
			slidingWindowOptions.MinStep,
			slidingWindowOptions.MaxStep,
			general.NewAggregatorSmoothWindow(general.SmoothWindowOpts{
				WindowSize: slidingWindowSize,
				TTL:        slidingWindowTTL, UsedMillValue: useMilliValue, AggregateFunc: slidingWindowOptions.AggregateFunc,
				AggregateArgs: slidingWindowOptions.AggregateArgs,
			}),
		),
		reportNUMASlidingWindow: make(map[int]general.SmoothWindow),
		emitter:                 emitter,
		getReclaimOptions:       getReclaimOptions,
		metaServer:              metaServer,
	}
}

func (m *GenericHeadroomManager) GetAllocatable() (resource.Quantity, error) {
	m.RLock()
	defer m.RUnlock()
	return m.getLastReportResult()
}

func (m *GenericHeadroomManager) GetCapacity() (resource.Quantity, error) {
	m.RLock()
	defer m.RUnlock()
	return m.getLastReportResult()
}

func (m *GenericHeadroomManager) GetNumaAllocatable() (map[int]resource.Quantity, error) {
	m.RLock()
	defer m.RUnlock()
	return m.getLastNUMAReportResult()
}

func (m *GenericHeadroomManager) GetNumaCapacity() (map[int]resource.Quantity, error) {
	m.RLock()
	defer m.RUnlock()
	return m.getLastNUMAReportResult()
}

func (m *GenericHeadroomManager) Run(ctx context.Context) {
	go wait.UntilWithContext(ctx, m.sync, m.syncPeriod)
	<-ctx.Done()
}

func (m *GenericHeadroomManager) getLastNUMAReportResult() (map[int]resource.Quantity, error) {
	if len(m.lastNUMAReportResult) == 0 {
		return nil, fmt.Errorf("resource %s last numa report value not found", m.resourceName)
	}
	return m.lastNUMAReportResult, nil
}

func (m *GenericHeadroomManager) getLastReportResult() (resource.Quantity, error) {
	if m.lastReportResult == nil {
		return resource.Quantity{}, fmt.Errorf("resource %s last report value not found", m.resourceName)
	}
	return m.reportResultTransformer(*m.lastReportResult), nil
}

func (m *GenericHeadroomManager) setLastReportResult(q resource.Quantity) {
	if m.lastReportResult == nil {
		m.lastReportResult = &resource.Quantity{}
	}
	q.DeepCopyInto(m.lastReportResult)
	m.emitResourceToMetric(metricsNameHeadroomReportResult, m.reportResultTransformer(*m.lastReportResult))
}

func (m *GenericHeadroomManager) newSlidingWindow() general.SmoothWindow {
	slidingWindowSize := int(m.slidingWindowOptions.SlidingWindowTime / m.syncPeriod)
	slidingWindowTTL := m.slidingWindowOptions.SlidingWindowTime * 2
	return general.NewCappedSmoothWindow(
		m.slidingWindowOptions.MinStep,
		m.slidingWindowOptions.MaxStep,
		general.NewAggregatorSmoothWindow(general.SmoothWindowOpts{
			WindowSize: slidingWindowSize,
			TTL:        slidingWindowTTL, UsedMillValue: m.useMilliValue, AggregateFunc: m.slidingWindowOptions.AggregateFunc,
			AggregateArgs: m.slidingWindowOptions.AggregateArgs,
		}),
	)
}

func (m *GenericHeadroomManager) sync(_ context.Context) {
	m.Lock()
	defer m.Unlock()

	reclaimOptions := m.getReclaimOptions()
	if !reclaimOptions.EnableReclaim {
		m.setLastReportResult(resource.Quantity{})

		for _, numaID := range m.metaServer.CPUDetails.NUMANodes().ToSliceInt() {
			m.lastNUMAReportResult[numaID] = resource.Quantity{}
		}
		return
	}

	var resourceName types.QoSResourceName
	switch m.resourceName {
	case v1.ResourceCPU:
		resourceName = types.QoSResourceCPU
	case v1.ResourceMemory:
		resourceName = types.QoSResourceMemory
	default:
		klog.Errorf("resource %v NOT support to get headroom", m.resourceName)
		return
	}

	subAdvisor, err := m.headroomAdvisor.GetSubAdvisor(resourceName)
	if err != nil {
		klog.Errorf("get SubAdvisor with resource %v failed: %v", resourceName, err)
		return
	}

	originResultFromAdvisor, numaResult, err := subAdvisor.GetHeadroom()
	if err != nil {
		klog.Errorf("get origin result %s from headroomAdvisor failed: %v", m.resourceName, err)
		return
	}

	reportResult := m.reportSlidingWindow.GetWindowedResources(originResultFromAdvisor)

	reportNUMAResult := make(map[int]*resource.Quantity)
	numaResultReady := true
	numaSum := 0.0
	reservedResourceForReportPerNUMA := *resource.NewQuantity(int64(float64(reclaimOptions.ReservedResourceForReport.Value())/float64(len(numaResult))), resource.DecimalSI)
	min := float64(reclaimOptions.MinReclaimedResourceForReport.Value()) / float64(len(numaResult))
	if reclaimOptions.MinReclaimedResourceForReport.Value() != 0 && min == 0 {
		min = 1
	}
	minReclaimedResourceForReportPerNUMA := *resource.NewQuantity(int64(min), resource.DecimalSI)
	for numaID, ret := range numaResult {
		numaWindow, ok := m.reportNUMASlidingWindow[numaID]
		if !ok {
			numaWindow = m.newSlidingWindow()
			m.reportNUMASlidingWindow[numaID] = numaWindow
		}

		result := numaWindow.GetWindowedResources(ret)
		if result == nil {
			klog.Infof("numa %d result if not ready", numaID)
			numaResultReady = false
			continue
		}

		result.Sub(reservedResourceForReportPerNUMA)
		if result.Cmp(minReclaimedResourceForReportPerNUMA) < 0 {
			result = &minReclaimedResourceForReportPerNUMA
		}
		reportNUMAResult[numaID] = result
		numaSum += float64(result.Value())
	}

	if reportResult == nil || !numaResultReady {
		klog.Infof("skip update reclaimed resource %s without enough valid sample: %v", m.resourceName, numaResultReady)
		return
	}

	reportResult.Sub(reclaimOptions.ReservedResourceForReport)
	if reportResult.Cmp(reclaimOptions.MinReclaimedResourceForReport) < 0 {
		reportResult = &reclaimOptions.MinReclaimedResourceForReport
	}

	klog.Infof("headroom manager for %s with originResultFromAdvisor: %s, reportResult: %s, "+
		"reservedResourceForReport: %s", m.resourceName, originResultFromAdvisor.String(),
		reportResult.String(), reclaimOptions.ReservedResourceForReport.String())

	m.setLastReportResult(*reportResult)

	// set latest numa report result
	diffRatio := float64(reportResult.Value()) / numaSum
	for numaID, res := range reportNUMAResult {
		res.Set(int64(float64(res.Value()) * diffRatio))
		result := m.reportResultTransformer(*res)
		m.lastNUMAReportResult[numaID] = result
		klog.Infof("%s headroom manager for NUMA: %d, headroom: %d", m.resourceName, numaID, result.Value())
	}
}

func (m *GenericHeadroomManager) emitResourceToMetric(metricsName string, value resource.Quantity) {
	_ = m.emitter.StoreInt64(metricsName, value.Value(), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "resourceName", Val: string(m.resourceName)})
}
