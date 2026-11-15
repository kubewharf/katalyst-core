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
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricsNameHeadroomReportResult      = "headroom_report_result"
	metricsNameHeadroomReportNUMAResult  = "headroom_report_numa_result"
	metricHeadroomApportionRequested     = "headroom_apportion_requested"
	metricHeadroomApportionEffective     = "headroom_apportion_effective"
	metricHeadroomApportionAlignmentLoss = "headroom_apportion_alignment_loss"
)

type GetGenericReclaimOptionsFunc func() GenericReclaimOptions

type NUMAResultApportioner func(
	target resource.Quantity,
	current map[int]resource.Quantity,
) (resource.Quantity, map[int]resource.Quantity, error)

type GenericHeadroomManagerOption func(*GenericHeadroomManager)

func WithNUMAResultApportioner(apportioner NUMAResultApportioner) GenericHeadroomManagerOption {
	return func(manager *GenericHeadroomManager) {
		manager.numaResultApportioner = apportioner
	}
}

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
	// the latest reporter result per numa before external unit transformation
	lastNUMAReportResult map[int]resource.Quantity

	metaServer              *metaserver.MetaServer
	metaCache               metacache.MetaCache
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
	numaResultApportioner   NUMAResultApportioner
}

func NewGenericHeadroomManager(name v1.ResourceName, useMilliValue, reportMilliValue bool,
	syncPeriod time.Duration, headroomAdvisor hmadvisor.ResourceAdvisor,
	emitter metrics.MetricEmitter, slidingWindowOptions GenericSlidingWindowOptions,
	getReclaimOptions GetGenericReclaimOptionsFunc,
	metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache,
	opts ...GenericHeadroomManagerOption,
) *GenericHeadroomManager {
	// Sliding window size and ttl are calculated by SlidingWindowTime and syncPeriod,
	// the valid lifetime of all samples is twice the duration of the sliding window.
	slidingWindowSize := int(slidingWindowOptions.SlidingWindowTime / syncPeriod)
	slidingWindowTTL := slidingWindowOptions.SlidingWindowTime * 2

	reportResultTransformer := func(quantity resource.Quantity) resource.Quantity {
		if reportMilliValue {
			return *resource.NewQuantity(quantity.MilliValue(), quantity.Format)
		}
		return quantity.DeepCopy()
	}

	manager := &GenericHeadroomManager{
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
		metaCache:               metaCache,
	}
	for _, opt := range opts {
		opt(manager)
	}
	return manager
}

func (m *GenericHeadroomManager) Name() v1.ResourceName {
	return m.resourceName
}

func (m *GenericHeadroomManager) MilliValue() bool {
	return m.useMilliValue
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
	result := make(map[int]resource.Quantity, len(m.lastNUMAReportResult))
	for numaID, quantity := range m.lastNUMAReportResult {
		result[numaID] = m.reportResultTransformer(quantity).DeepCopy()
	}
	return result, nil
}

func (m *GenericHeadroomManager) getLastReportResult() (resource.Quantity, error) {
	if m.lastReportResult == nil {
		return resource.Quantity{}, fmt.Errorf("resource %s last report value not found", m.resourceName)
	}
	return m.reportResultTransformer(*m.lastReportResult).DeepCopy(), nil
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
			m.emitNUMAResourceToMetric(numaID, metricsNameHeadroomReportNUMAResult, resource.Quantity{})
		}
		return
	}

	subAdvisor, err := m.headroomAdvisor.GetSubAdvisor(types.QoSResourceName(m.resourceName))
	if err != nil {
		klog.Errorf("get SubAdvisor with resource %v failed: %v", m.resourceName, err)
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

	allocations := make(map[int]resource.Quantity, len(reportNUMAResult))
	var apportionRequested *resource.Quantity
	if m.numaResultApportioner != nil {
		validationBaseline := make(map[int]resource.Quantity, len(reportNUMAResult))
		for numaID, result := range reportNUMAResult {
			validationBaseline[numaID] = result.DeepCopy()
		}
		strategyInput := make(map[int]resource.Quantity, len(validationBaseline))
		for numaID, limit := range validationBaseline {
			strategyInput[numaID] = limit.DeepCopy()
		}

		requested := reportResult.DeepCopy()
		apportionRequested = &requested
		effective, apportioned, apportionErr := m.numaResultApportioner(requested.DeepCopy(), strategyInput)
		if apportionErr != nil {
			klog.Errorf("apportion numa result failed: %v", apportionErr)
			return
		}
		if err := validateNUMAResult(requested, validationBaseline, effective, apportioned); err != nil {
			klog.Errorf("validate apportioned numa result failed: %v", err)
			return
		}
		effective = effective.DeepCopy()
		reportResult = &effective
		for numaID, allocation := range apportioned {
			allocations[numaID] = allocation.DeepCopy()
		}
	} else {
		diffRatio := float64(reportResult.Value()) / numaSum
		for numaID, result := range reportNUMAResult {
			if result.Value() != 0 {
				result.Set(int64(float64(result.Value()) * diffRatio))
			}
			allocations[numaID] = result.DeepCopy()
		}
	}

	headroomInfo := &types.HeadroomInfo{
		TotalHeadroom: float64(reportResult.MilliValue()) / 1000,
		NUMAHeadroom:  make(map[int]float64, len(allocations)),
	}
	for numaID, quantity := range allocations {
		headroomInfo.NUMAHeadroom[numaID] = float64(quantity.MilliValue()) / 1000
	}

	if err = m.metaCache.SetHeadroomEntries(string(m.resourceName), headroomInfo); err != nil {
		klog.Errorf("set headroom entries failed: %v", err)
		return
	}

	if apportionRequested != nil {
		m.emitCPUApportionMetrics(*apportionRequested, *reportResult)
	}
	m.setLastReportResult(*reportResult)
	m.lastNUMAReportResult = allocations
	for numaID, quantity := range allocations {
		result := m.reportResultTransformer(quantity)
		m.emitNUMAResourceToMetric(numaID, metricsNameHeadroomReportNUMAResult, result)
		klog.V(4).Infof("%s headroom manager for NUMA: %d, headroom: %d", m.resourceName, numaID, result.Value())
	}
}

func validateNUMAResult(
	requested resource.Quantity,
	current map[int]resource.Quantity,
	effective resource.Quantity,
	allocations map[int]resource.Quantity,
) error {
	zero := resource.Quantity{}
	if effective.Cmp(zero) < 0 {
		return fmt.Errorf("effective target %s is negative", effective.String())
	}
	if effective.Cmp(requested) > 0 {
		return fmt.Errorf("effective target %s exceeds requested target %s", effective.String(), requested.String())
	}
	if len(allocations) != len(current) {
		return fmt.Errorf("numa allocation keys do not match current keys")
	}

	sum := resource.Quantity{}
	for numaID, limit := range current {
		allocation, ok := allocations[numaID]
		if !ok {
			return fmt.Errorf("numa allocation is missing key %d", numaID)
		}
		if allocation.Cmp(zero) < 0 {
			return fmt.Errorf("numa %d allocation %s is negative", numaID, allocation.String())
		}
		if allocation.Cmp(limit) > 0 {
			return fmt.Errorf("numa %d allocation %s exceeds current limit %s",
				numaID, allocation.String(), limit.String())
		}
		sum.Add(allocation)
	}
	if sum.Cmp(effective) != 0 {
		return fmt.Errorf("numa allocation sum %s does not equal effective target %s", sum.String(), effective.String())
	}
	return nil
}

func (m *GenericHeadroomManager) emitCPUApportionMetrics(requested, effective resource.Quantity) {
	tags := []metrics.MetricTag{
		{Key: "component", Val: "reporter"},
		{Key: "resource", Val: "cpu"},
	}
	requestedCPU := requested.MilliValue() / 1000
	effectiveCPU := effective.MilliValue() / 1000
	_ = m.emitter.StoreInt64(metricHeadroomApportionRequested, requestedCPU, metrics.MetricTypeNameRaw, tags...)
	_ = m.emitter.StoreInt64(metricHeadroomApportionEffective, effectiveCPU, metrics.MetricTypeNameRaw, tags...)
	_ = m.emitter.StoreInt64(metricHeadroomApportionAlignmentLoss, requestedCPU-effectiveCPU, metrics.MetricTypeNameRaw, tags...)
}

func (m *GenericHeadroomManager) emitResourceToMetric(metricsName string, value resource.Quantity) {
	_ = m.emitter.StoreInt64(metricsName, value.Value(), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "resourceName", Val: string(m.resourceName)})
}

func (m *GenericHeadroomManager) emitNUMAResourceToMetric(numaID int, metricsName string, value resource.Quantity) {
	_ = m.emitter.StoreInt64(metricsName, value.Value(), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "resourceName", Val: string(m.resourceName)},
		metrics.MetricTag{Key: "numa", Val: strconv.Itoa(numaID)})
}
