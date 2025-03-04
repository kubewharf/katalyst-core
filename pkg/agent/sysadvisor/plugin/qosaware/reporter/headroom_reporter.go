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
	"sync"

	v1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	nodeapis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter/manager"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter/manager/resource"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func init() {
	manager.RegisterHeadroomManagerInitializer(apiconsts.ReclaimedResourceMilliCPU, resource.NewCPUHeadroomManager)
	manager.RegisterHeadroomManagerInitializer(apiconsts.ReclaimedResourceMemory, resource.NewMemoryHeadroomManager)
}

const (
	headroomReporterPluginName = "headroom-reporter-plugin"
)

type HeadroomResourceManager interface {
	manager.ResourceManager
	manager.NumaResourceManager
}

type HeadroomResourceGetter interface {
	GetHeadroomResource(name v1.ResourceName) (HeadroomResourceManager, error)
}

type HeadroomReporter struct {
	skeleton.GenericPlugin
	HeadroomResourceGetter
}

type DummyHeadroomResourceManager struct{}

func (mgr *DummyHeadroomResourceManager) GetAllocatable() (apiresource.Quantity, error) {
	return apiresource.Quantity{}, nil
}

func (mgr *DummyHeadroomResourceManager) GetCapacity() (apiresource.Quantity, error) {
	return apiresource.Quantity{}, nil
}

func (mgr *DummyHeadroomResourceManager) GetNumaAllocatable() (map[int]apiresource.Quantity, error) {
	return nil, nil
}

func (mgr *DummyHeadroomResourceManager) GetNumaCapacity() (map[int]apiresource.Quantity, error) {
	return nil, nil
}

// NewHeadroomReporter returns a wrapper of headroom reporter plugins as headroom reporter
func NewHeadroomReporter(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, headroomAdvisor hmadvisor.ResourceAdvisor,
) (*HeadroomReporter, error) {
	plugin, getter, err := newHeadroomReporterPlugin(emitter, metaServer, conf, headroomAdvisor)
	if err != nil {
		return nil, fmt.Errorf("[headroom-reporter] create headroom reporter failed: %s", err)
	}

	return &HeadroomReporter{GenericPlugin: plugin, HeadroomResourceGetter: getter}, nil
}

func (r *HeadroomReporter) Run(ctx context.Context) {
	if err := r.Start(); err != nil {
		klog.Fatalf("[headroom-reporter] start %v failed: %v", r.Name(), err)
	}
	klog.Infof("[headroom-reporter] plugin wrapper %s started", r.Name())

	<-ctx.Done()
	if err := r.Stop(); err != nil {
		klog.Errorf("[headroom-reporter] stop %v failed: %v", r.Name(), err)
	}
}

type reclaimedResource struct {
	allocatable     v1.ResourceList
	capacity        v1.ResourceList
	numaAllocatable map[int]v1.ResourceList
	numaCapacity    map[int]v1.ResourceList

	resourceNameMap map[v1.ResourceName]v1.ResourceName
	milliValue      map[v1.ResourceName]bool
}

type headroomReporterPlugin struct {
	sync.Mutex
	headroomManagers      map[v1.ResourceName]manager.HeadroomManager
	numaSocketZoneNodeMap map[util.ZoneNode]util.ZoneNode

	dynamicConf *dynamic.DynamicAgentConfiguration
	ctx         context.Context
	cancel      context.CancelFunc
	started     bool
}

func newHeadroomReporterPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, headroomAdvisor hmadvisor.ResourceAdvisor,
) (skeleton.GenericPlugin, HeadroomResourceGetter, error) {
	var (
		err     error
		errList []error
	)

	// init numa topo info by metaServer
	if metaServer == nil || metaServer.MachineInfo == nil {
		return nil, nil, fmt.Errorf("get metaserver machine info is nil")
	}

	initializers := manager.GetRegisteredManagerInitializers()
	headroomManagers := make(map[v1.ResourceName]manager.HeadroomManager, len(initializers))
	for name, initializer := range initializers {
		headroomManagers[name], err = initializer(emitter, metaServer, conf, headroomAdvisor)
		if err != nil {
			errList = append(errList, err)
		}
	}

	if len(errList) > 0 {
		return nil, nil, errors.NewAggregate(errList)
	}

	reporter := &headroomReporterPlugin{
		headroomManagers:      headroomManagers,
		numaSocketZoneNodeMap: util.GenerateNumaSocketZone(metaServer.MachineInfo.Topology),
		dynamicConf:           conf.DynamicAgentConfiguration,
	}
	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(reporter, []string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"pluginName": headroomReporterPluginName,
				"pluginType": registration.ReporterPlugin,
			})...)
		})
	if err != nil {
		return nil, nil, err
	}

	return pluginWrapper, reporter, nil
}

func (r *headroomReporterPlugin) GetHeadroomResource(name v1.ResourceName) (HeadroomResourceManager, error) {
	if mgr, ok := r.headroomManagers[name]; ok {
		return mgr, nil
	}

	return nil, fmt.Errorf("not found headroom manager for resource %s", name)
}

func (r *headroomReporterPlugin) Name() string {
	return headroomReporterPluginName
}

func (r *headroomReporterPlugin) Start() (err error) {
	r.Lock()
	defer func() {
		if err == nil {
			r.started = true
		}
		r.Unlock()
	}()

	if r.started {
		return
	}

	r.ctx, r.cancel = context.WithCancel(context.Background())
	for _, rm := range r.headroomManagers {
		go rm.Run(r.ctx)
	}
	return
}

func (r *headroomReporterPlugin) Stop() error {
	r.Lock()
	defer func() {
		r.started = false
		r.Unlock()
	}()

	// plugin.Stop may be called before plugin.Start or multiple times,
	// we should ensure cancel function exists
	if !r.started {
		return nil
	}

	r.cancel()
	return nil
}

func (r *headroomReporterPlugin) GetReportContent(_ context.Context, _ *v1alpha1.Empty) (*v1alpha1.GetReportContentResponse, error) {
	var err error
	res, err := r.getReclaimedResource()
	if err != nil {
		return nil, err
	}

	// revise reclaimed resource to avoid resource fragmentation
	err = r.reviseReclaimedResource(res)
	if err != nil {
		return nil, err
	}

	reportToCNR, err := r.getReportReclaimedResourceForCNR(res)
	if err != nil {
		return nil, err
	}

	return &v1alpha1.GetReportContentResponse{
		Content: []*v1alpha1.ReportContent{
			reportToCNR,
		},
	}, nil
}

func (r *headroomReporterPlugin) ListAndWatchReportContent(_ *v1alpha1.Empty, server v1alpha1.ReporterPlugin_ListAndWatchReportContentServer) error {
	for {
		select {
		case <-r.ctx.Done():
			return nil
		case <-server.Context().Done():
			return nil
		}
	}
}

func (r *headroomReporterPlugin) getReclaimedResource() (*reclaimedResource, error) {
	var (
		err     error
		errList []error
	)

	allocatable := make(v1.ResourceList)
	capacity := make(v1.ResourceList)
	numaAllocatable := make(map[int]v1.ResourceList)
	numaCapacity := make(map[int]v1.ResourceList)
	resourceNameMap := make(map[v1.ResourceName]v1.ResourceName)
	milliValue := make(map[v1.ResourceName]bool)
	for reportName, rm := range r.headroomManagers {
		// get origin resource name
		resourceNameMap[reportName] = rm.Name()
		milliValue[reportName] = rm.MilliValue()
		allocatable[reportName], err = rm.GetAllocatable()
		if err != nil {
			errList = append(errList, fmt.Errorf("get reclaimed %s allocatable failed: %s", reportName, err))
		}

		capacity[reportName], err = rm.GetCapacity()
		if err != nil {
			errList = append(errList, err, fmt.Errorf("get reclaimed %s capacity failed: %s", reportName, err))
		}

		// get allocatable per numa
		allocatableMap, err := rm.GetNumaAllocatable()
		if err != nil {
			errList = append(errList, fmt.Errorf("get reclaimed %s numa allocatable failed: %s", reportName, err))
		} else {
			for numaID, quantity := range allocatableMap {
				perNumaAllocatable, ok := numaAllocatable[numaID]
				if !ok {
					perNumaAllocatable = make(v1.ResourceList)
					numaAllocatable[numaID] = perNumaAllocatable
				}
				perNumaAllocatable[reportName] = quantity
			}
		}

		// get capacity per numa
		capacityMap, err := rm.GetNumaCapacity()
		if err != nil {
			errList = append(errList, fmt.Errorf("get reclaimed %s numa capacity failed: %s", reportName, err))
		} else {
			for numaID, quantity := range capacityMap {
				perNumaCapacity, ok := numaCapacity[numaID]
				if !ok {
					perNumaCapacity = make(v1.ResourceList)
					numaCapacity[numaID] = perNumaCapacity
				}
				perNumaCapacity[reportName] = quantity
			}
		}
	}

	if len(errList) > 0 {
		return nil, errors.NewAggregate(errList)
	}

	return &reclaimedResource{
		allocatable:     allocatable,
		capacity:        capacity,
		numaAllocatable: numaAllocatable,
		numaCapacity:    numaCapacity,
		milliValue:      milliValue,
		resourceNameMap: resourceNameMap,
	}, nil
}

func (r *headroomReporterPlugin) getReportReclaimedResourceForCNR(reclaimedResource *reclaimedResource) (*v1alpha1.ReportContent, error) {
	if reclaimedResource == nil {
		return nil, nil
	}

	resourceField, err := r.getReportReclaimedResource(reclaimedResource)
	if err != nil {
		return nil, err
	}

	topologyZoneField, err := r.getReportNUMAReclaimedResource(reclaimedResource)
	if err != nil {
		return nil, err
	}

	return &v1alpha1.ReportContent{
		GroupVersionKind: &util.CNRGroupVersionKind,
		Field: []*v1alpha1.ReportField{
			resourceField, topologyZoneField,
		},
	}, nil
}

func (r *headroomReporterPlugin) getReportReclaimedResource(reclaimedResource *reclaimedResource) (*v1alpha1.ReportField, error) {
	resources := nodeapis.Resources{
		Allocatable: &reclaimedResource.allocatable,
		Capacity:    &reclaimedResource.capacity,
	}

	resourcesValue, err := json.Marshal(&resources)
	if err != nil {
		return nil, fmt.Errorf("marshal resource failed: %s", err)
	}

	return &v1alpha1.ReportField{
		FieldType: v1alpha1.FieldType_Status,
		FieldName: util.CNRFieldNameResources,
		Value:     resourcesValue,
	}, nil
}

func (r *headroomReporterPlugin) getReportNUMAReclaimedResource(reclaimedResource *reclaimedResource) (*v1alpha1.ReportField, error) {
	topologyZoneGenerator, err := util.NewNumaSocketTopologyZoneGenerator(r.numaSocketZoneNodeMap)
	if err != nil {
		return nil, fmt.Errorf("create topology zone generator failed: %s", err)
	}

	zoneResources := make(map[util.ZoneNode]nodeapis.Resources)
	for numaID := range reclaimedResource.numaAllocatable {
		allocatable := reclaimedResource.numaAllocatable[numaID]
		capacity, ok := reclaimedResource.numaCapacity[numaID]
		if !ok {
			return nil, fmt.Errorf("miss capacity with numaID: %d", numaID)
		}

		numaZoneNode := util.GenerateNumaZoneNode(numaID)
		zoneResources[numaZoneNode] = nodeapis.Resources{
			Allocatable: &allocatable,
			Capacity:    &capacity,
		}
	}

	topologyZone := topologyZoneGenerator.GenerateTopologyZoneStatus(nil, zoneResources, nil, nil)
	value, err := json.Marshal(&topologyZone)
	if err != nil {
		return nil, fmt.Errorf("marshal topology zone failed: %s", err)
	}

	return &v1alpha1.ReportField{
		FieldType: v1alpha1.FieldType_Status,
		FieldName: util.CNRFieldNameTopologyZone,
		Value:     value,
	}, nil
}

func (r *headroomReporterPlugin) reviseReclaimedResource(res *reclaimedResource) error {
	if res == nil {
		return fmt.Errorf("reclaimed resource is nil")
	}

	conf := r.dynamicConf.GetDynamicConfiguration()
	reviseFunc := func(resList v1.ResourceList) bool {
		revise := false
		for reportName, quantity := range resList {
			resourceName, ok := res.resourceNameMap[reportName]
			if !ok {
				resourceName = reportName
			}

			minIgnored, ok := conf.MinIgnoredReclaimedResourceForReport[resourceName]
			if ok {
				milliValue, ok := res.milliValue[reportName]
				if ok && milliValue {
					minIgnored = *apiresource.NewQuantity(minIgnored.MilliValue(), minIgnored.Format)
				}

				if quantity.Cmp(minIgnored) <= 0 {
					revise = true
					break
				}
			}
		}

		if revise {
			for resourceName := range resList {
				resList[resourceName] = apiresource.Quantity{}
			}
		}

		return revise
	}

	numaRevised := false
	for numaID := range res.numaAllocatable {
		if reviseFunc(res.numaAllocatable[numaID]) {
			numaRevised = true
		}
	}

	if numaRevised {
		sumNUMAAllocatable := v1.ResourceList{}
		for _, allocatable := range res.numaAllocatable {
			sumNUMAAllocatable = native.AddResources(sumNUMAAllocatable, allocatable)
		}
		res.allocatable = sumNUMAAllocatable
	}

	reviseFunc(res.allocatable)
	return nil
}
