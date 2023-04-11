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

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter/manager/broker"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
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
	MaxStep resource.Quantity
}

type GenericHeadroomManager struct {
	sync.RWMutex
	lastReportResult *resource.Quantity

	headroomAdvisor     hmadvisor.ResourceAdvisor
	broker              broker.Broker
	emitter             metrics.MetricEmitter
	originSlidingWindow general.SmoothWindow
	reportSlidingWindow general.SmoothWindow

	reportResultTransformer func(quantity resource.Quantity) resource.Quantity
	resourceName            v1.ResourceName
	syncPeriod              time.Duration
	getReclaimOptions       GetGenericReclaimOptionsFunc
}

func NewGenericHeadroomManager(name v1.ResourceName, useMilliValue, reportMilliValue bool,
	syncPeriod time.Duration, broker broker.Broker, headroomAdvisor hmadvisor.ResourceAdvisor,
	emitter metrics.MetricEmitter, slidingWindowOptions GenericSlidingWindowOptions,
	getReclaimOptions GetGenericReclaimOptionsFunc) *GenericHeadroomManager {

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
		reportResultTransformer: reportResultTransformer,
		broker:                  broker,
		syncPeriod:              syncPeriod,
		headroomAdvisor:         headroomAdvisor,
		originSlidingWindow: general.NewCappedSmoothWindow(
			slidingWindowOptions.MinStep,
			slidingWindowOptions.MaxStep,
			general.NewAverageWithTTLSmoothWindow(slidingWindowSize, slidingWindowTTL, useMilliValue),
		),
		reportSlidingWindow: general.NewCappedSmoothWindow(
			slidingWindowOptions.MinStep,
			slidingWindowOptions.MaxStep,
			general.NewAverageWithTTLSmoothWindow(slidingWindowSize, slidingWindowTTL, useMilliValue),
		),
		emitter:           emitter,
		getReclaimOptions: getReclaimOptions,
	}
}

func (m *GenericHeadroomManager) ResourceName() v1.ResourceName {
	return m.resourceName
}

func (m *GenericHeadroomManager) GetAllocatable() (resource.Quantity, error) {
	return m.getLastReportResult()
}

func (m *GenericHeadroomManager) GetCapacity() (resource.Quantity, error) {
	return m.getLastReportResult()
}

func (m *GenericHeadroomManager) Run(ctx context.Context) {
	go m.broker.Run(ctx)
	go wait.UntilWithContext(ctx, m.sync, m.syncPeriod)
	<-ctx.Done()
}

func (m *GenericHeadroomManager) getLastReportResult() (resource.Quantity, error) {
	m.RLock()
	lastReportResult := m.lastReportResult
	m.RUnlock()
	if lastReportResult == nil {
		return resource.Quantity{}, fmt.Errorf("resource %s last report value not found", m.resourceName)
	}
	return m.reportResultTransformer(lastReportResult.DeepCopy()), nil
}

func (m *GenericHeadroomManager) sync(_ context.Context) {
	m.Lock()
	defer m.Unlock()

	reclaimOptions := m.getReclaimOptions()
	if !reclaimOptions.EnableReclaim {
		m.lastReportResult = &resource.Quantity{}
		return
	}

	originResultFromAdvisor, err := m.headroomAdvisor.GetHeadroom(m.resourceName)
	if err != nil {
		klog.Errorf("get origin result %s from headroomAdvisor failed: %v", m.resourceName, err)
		return
	}

	reportResultFromBroker, err := m.broker.Calculate(originResultFromAdvisor)
	if err != nil {
		klog.Errorf("calculate report result %s from broker failed: %v", m.resourceName, err)
		return
	}

	reportResult := m.reportSlidingWindow.GetWindowedResources(reportResultFromBroker)
	if reportResult == nil {
		klog.Infof("skip update reclaimed resource %s without enough valid sample", m.resourceName)
		return
	}

	reportResult.Sub(reclaimOptions.ReservedResourceForReport)
	if reportResult.Cmp(reclaimOptions.MinReclaimedResourceForReport) < 0 {
		reportResult = &reclaimOptions.MinReclaimedResourceForReport
	}

	klog.Infof("headroom manager for %s with originResultFromAdvisor: %s, reportResultFromBroker: %s, "+
		"reportResult: %s, reservedResourceForReport: %s", m.resourceName, originResultFromAdvisor.String(),
		reportResultFromBroker.String(), reportResult.String(), reclaimOptions.ReservedResourceForReport.String())

	m.emitResourceToMetric(metricsNameHeadroomReportResult, m.reportResultTransformer(*reportResult))

	m.lastReportResult = reportResult
}

func (m *GenericHeadroomManager) emitResourceToMetric(metricsName string, value resource.Quantity) {
	_ = m.emitter.StoreInt64(metricsName, value.Value(), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "resourceName", Val: string(m.resourceName)})
}
