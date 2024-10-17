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

package metric

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/syntax"
)

type MetricsNotifierManagerImpl struct {
	*syntax.RWMutex
	metricStore        *utilmetric.MetricStore
	registeredNotifier map[types.MetricsScope]map[string]*types.NotifiedData
}

func NewMetricsNotifierManager(metricStore *utilmetric.MetricStore, emitter metrics.MetricEmitter) types.MetricsNotifierManager {
	return &MetricsNotifierManagerImpl{
		metricStore: metricStore,
		RWMutex:     syntax.NewRWMutex(emitter),
		registeredNotifier: map[types.MetricsScope]map[string]*types.NotifiedData{
			types.MetricsScopeNode:          make(map[string]*types.NotifiedData),
			types.MetricsScopeNuma:          make(map[string]*types.NotifiedData),
			types.MetricsScopeCPU:           make(map[string]*types.NotifiedData),
			types.MetricsScopeDevice:        make(map[string]*types.NotifiedData),
			types.MetricsScopeContainer:     make(map[string]*types.NotifiedData),
			types.MetricsScopeContainerNUMA: make(map[string]*types.NotifiedData),
		},
	}
}

func (m *MetricsNotifierManagerImpl) RegisterNotifier(scope types.MetricsScope, req types.NotifiedRequest,
	response chan types.NotifiedResponse,
) string {
	if _, ok := m.registeredNotifier[scope]; !ok {
		return ""
	}

	m.Lock()
	defer m.Unlock()

	randBytes := make([]byte, 30)
	rand.Read(randBytes)
	key := string(randBytes)

	m.registeredNotifier[scope][key] = &types.NotifiedData{
		Scope:    scope,
		Req:      req,
		Response: response,
	}
	return key
}

func (m *MetricsNotifierManagerImpl) DeRegisterNotifier(scope types.MetricsScope, key string) {
	m.Lock()
	defer m.Unlock()

	delete(m.registeredNotifier[scope], key)
}

func (m *MetricsNotifierManagerImpl) Notify() {
	m.notifySystem()
	m.notifyPods()
}

// notifySystem notifies system-related data
func (m *MetricsNotifierManagerImpl) notifySystem() {
	now := time.Now()
	m.RLock()
	defer m.RUnlock()

	m.processRegisteredNotifier(types.MetricsScopeNode, now, func(reg *types.NotifiedData) (utilmetric.MetricData, error) {
		return m.metricStore.GetNodeMetric(reg.Req.MetricName)
	})

	m.processRegisteredNotifier(types.MetricsScopeDevice, now, func(reg *types.NotifiedData) (utilmetric.MetricData, error) {
		return m.metricStore.GetDeviceMetric(reg.Req.DeviceID, reg.Req.MetricName)
	})

	m.processRegisteredNotifier(types.MetricsScopeNuma, now, func(reg *types.NotifiedData) (utilmetric.MetricData, error) {
		return m.metricStore.GetNumaMetric(reg.Req.NumaID, reg.Req.MetricName)
	})

	m.processRegisteredNotifier(types.MetricsScopeCPU, now, func(reg *types.NotifiedData) (utilmetric.MetricData, error) {
		return m.metricStore.GetCPUMetric(reg.Req.CoreID, reg.Req.MetricName)
	})
}

// notifySystem notifies pod-related data
func (m *MetricsNotifierManagerImpl) notifyPods() {
	now := time.Now()
	m.RLock()
	defer m.RUnlock()

	m.processRegisteredNotifier(types.MetricsScopeContainer, now, func(reg *types.NotifiedData) (utilmetric.MetricData, error) {
		return m.metricStore.GetContainerMetric(reg.Req.PodUID, reg.Req.ContainerName, reg.Req.MetricName)
	})

	m.processRegisteredNotifier(types.MetricsScopeContainerNUMA, now, func(reg *types.NotifiedData) (utilmetric.MetricData, error) {
		return m.metricStore.GetContainerNumaMetric(reg.Req.PodUID, reg.Req.ContainerName, fmt.Sprintf("%v", reg.Req.NumaNode), reg.Req.MetricName)
	})
}

func (m *MetricsNotifierManagerImpl) processRegisteredNotifier(scope types.MetricsScope, now time.Time, getMetricFunc func(reg *types.NotifiedData) (utilmetric.MetricData, error)) {
	for _, reg := range m.registeredNotifier[scope] {
		v, err := getMetricFunc(reg)
		if err != nil {
			continue
		} else if v.Time == nil {
			v.Time = &now
		}

		if reg.LastNotify.Equal(*v.Time) {
			continue
		} else {
			reg.LastNotify = *v.Time
		}

		reg.Response <- types.NotifiedResponse{
			Req:        reg.Req,
			MetricData: v,
		}
	}
}

type ExternalMetricManagerImpl struct {
	*syntax.RWMutex
	metricStore      *utilmetric.MetricStore
	registeredMetric []func(store *utilmetric.MetricStore)
}

func NewExternalMetricManager(metricStore *utilmetric.MetricStore, emitter metrics.MetricEmitter) types.ExternalMetricManager {
	return &ExternalMetricManagerImpl{
		metricStore: metricStore,
		RWMutex:     syntax.NewRWMutex(emitter),
	}
}

func (m *ExternalMetricManagerImpl) RegisterExternalMetric(f func(store *utilmetric.MetricStore)) {
	m.Lock()
	defer m.Unlock()
	m.registeredMetric = append(m.registeredMetric, f)
}

func (m *ExternalMetricManagerImpl) Sample() {
	m.RLock()
	defer m.RUnlock()
	for _, f := range m.registeredMetric {
		f(m.metricStore)
	}
}

type MetricsFetcherImpl struct {
	startOnce sync.Once
	hasSynced bool

	metricStore            *utilmetric.MetricStore
	metricsNotifierManager types.MetricsNotifierManager
	externalMetricManager  types.ExternalMetricManager
	checkMetricDataExpire  CheckMetricDataExpireFunc

	defaultInterval time.Duration
	provisioners    map[string]types.MetricsProvisioner
	intervals       map[string]time.Duration
}

func NewMetricsFetcher(baseConf *global.BaseConfiguration, metricConf *metaserver.MetricConfiguration, emitter metrics.MetricEmitter, podFetcher pod.PodFetcher) types.MetricsFetcher {
	metricStore := utilmetric.NewMetricStore()
	metricsNotifierManager := NewMetricsNotifierManager(metricStore, emitter)
	externalMetricManager := NewExternalMetricManager(metricStore, emitter)

	intervals := make(map[string]time.Duration)
	provisioners := make(map[string]types.MetricsProvisioner)
	registeredProvisioners := getProvisioners()
	for _, name := range metricConf.MetricProvisions {
		if f, ok := registeredProvisioners[name]; ok {
			intervals[name] = metricConf.DefaultInterval
			if interval, exist := metricConf.ProvisionerIntervals[name]; exist {
				intervals[name] = interval
			}
			provisioners[name] = f(baseConf, metricConf, emitter, podFetcher, metricStore)
		}
	}

	return &MetricsFetcherImpl{
		metricStore:            metricStore,
		metricsNotifierManager: metricsNotifierManager,
		externalMetricManager:  externalMetricManager,
		checkMetricDataExpire:  checkMetricDataExpireFunc(metricConf.MetricInsurancePeriod),

		defaultInterval: metricConf.DefaultInterval,
		provisioners:    provisioners,
		intervals:       intervals,
	}
}

func (f *MetricsFetcherImpl) GetNodeMetric(metricName string) (utilmetric.MetricData, error) {
	return f.checkMetricDataExpire(f.metricStore.GetNodeMetric(metricName))
}

func (f *MetricsFetcherImpl) GetNumaMetric(numaID int, metricName string) (utilmetric.MetricData, error) {
	return f.checkMetricDataExpire(f.metricStore.GetNumaMetric(numaID, metricName))
}

func (f *MetricsFetcherImpl) GetDeviceMetric(deviceName string, metricName string) (utilmetric.MetricData, error) {
	return f.checkMetricDataExpire(f.metricStore.GetDeviceMetric(deviceName, metricName))
}

func (f *MetricsFetcherImpl) GetCPUMetric(coreID int, metricName string) (utilmetric.MetricData, error) {
	return f.checkMetricDataExpire(f.metricStore.GetCPUMetric(coreID, metricName))
}

func (f *MetricsFetcherImpl) GetContainerMetric(podUID, containerName, metricName string) (utilmetric.MetricData, error) {
	return f.checkMetricDataExpire(f.metricStore.GetContainerMetric(podUID, containerName, metricName))
}

func (f *MetricsFetcherImpl) GetContainerNumaMetric(podUID, containerName, numaNode, metricName string) (utilmetric.MetricData, error) {
	return f.checkMetricDataExpire(f.metricStore.GetContainerNumaMetric(podUID, containerName, numaNode, metricName))
}

func (f *MetricsFetcherImpl) GetPodVolumeMetric(podUID, volumeName, metricName string) (utilmetric.MetricData, error) {
	return f.checkMetricDataExpire(f.metricStore.GetPodVolumeMetric(podUID, volumeName, metricName))
}

func (f *MetricsFetcherImpl) GetCgroupMetric(cgroupPath, metricName string) (utilmetric.MetricData, error) {
	return f.checkMetricDataExpire(f.metricStore.GetCgroupMetric(cgroupPath, metricName))
}

func (f *MetricsFetcherImpl) GetCgroupNumaMetric(cgroupPath string, numaNode int, metricName string) (utilmetric.MetricData, error) {
	return f.checkMetricDataExpire(f.metricStore.GetCgroupNumaMetric(cgroupPath, numaNode, metricName))
}

func (f *MetricsFetcherImpl) AggregatePodNumaMetric(podList []*v1.Pod, numaNode, metricName string,
	agg utilmetric.Aggregator, filter utilmetric.ContainerMetricFilter,
) utilmetric.MetricData {
	return f.metricStore.AggregatePodNumaMetric(podList, numaNode, metricName, agg, filter)
}

func (f *MetricsFetcherImpl) AggregatePodMetric(podList []*v1.Pod, metricName string,
	agg utilmetric.Aggregator, filter utilmetric.ContainerMetricFilter,
) utilmetric.MetricData {
	return f.metricStore.AggregatePodMetric(podList, metricName, agg, filter)
}

func (f *MetricsFetcherImpl) AggregateCoreMetric(cpuset machine.CPUSet, metricName string, agg utilmetric.Aggregator) utilmetric.MetricData {
	return f.metricStore.AggregateCoreMetric(cpuset, metricName, agg)
}

func (f *MetricsFetcherImpl) RegisterNotifier(scope types.MetricsScope, req types.NotifiedRequest, response chan types.NotifiedResponse) string {
	return f.metricsNotifierManager.RegisterNotifier(scope, req, response)
}

func (f *MetricsFetcherImpl) DeRegisterNotifier(scope types.MetricsScope, key string) {
	f.metricsNotifierManager.DeRegisterNotifier(scope, key)
}

func (f *MetricsFetcherImpl) RegisterExternalMetric(externalMetricFunc func(store *utilmetric.MetricStore)) {
	f.externalMetricManager.RegisterExternalMetric(externalMetricFunc)
}

func (f *MetricsFetcherImpl) Run(ctx context.Context) {
	// make sure all provisioners have started at least once,
	// and then allow each provisioner to collect metrics with
	// its specified period.
	// whenever any provisioner finishes its collecting process,
	// notification will be triggered, and the consumer should
	// handler duplication logic if necessary.
	f.startOnce.Do(func() {
		f.init(ctx)
		f.run(ctx)
	})
}

func (f *MetricsFetcherImpl) init(ctx context.Context) {
	wg := sync.WaitGroup{}
	for name := range f.provisioners {
		p := f.provisioners[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.Run(ctx)
		}()
	}
	wg.Wait()

	if f.externalMetricManager != nil {
		f.externalMetricManager.Sample()
	}

	if f.metricsNotifierManager != nil {
		f.metricsNotifierManager.Notify()
	}

	if !f.hasSynced {
		f.hasSynced = true
	}
}

func (f *MetricsFetcherImpl) run(ctx context.Context) {
	// provisioner's implementation and its interval always exist,
	// and it's ensured in init function
	for name := range f.provisioners {
		p := f.provisioners[name]
		t := f.intervals[name]
		go wait.Until(func() {
			p.Run(ctx)
			if f.metricsNotifierManager != nil {
				f.metricsNotifierManager.Notify()
			}
		}, t, ctx.Done())
	}

	if f.externalMetricManager != nil {
		go wait.Until(func() {
			f.externalMetricManager.Sample()
			if f.metricsNotifierManager != nil {
				f.metricsNotifierManager.Notify()
			}
		}, f.defaultInterval, ctx.Done())
	}
}

func (f *MetricsFetcherImpl) HasSynced() bool {
	return f.hasSynced
}
