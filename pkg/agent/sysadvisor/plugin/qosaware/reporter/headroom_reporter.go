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
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

func init() {
	manager.RegisterHeadroomManagerInitializer(apiconsts.ReclaimedResourceMilliCPU, resource.NewCPUHeadroomManager)
	manager.RegisterHeadroomManagerInitializer(apiconsts.ReclaimedResourceMemory, resource.NewMemoryHeadroomManager)
}

const (
	headroomReporterPluginName = "headroom-reporter-plugin"
)

type headroomReporterImpl struct {
	skeleton.GenericPlugin
}

// NewHeadroomReporter returns a wrapper of headroom reporter plugins as headroom reporter
func NewHeadroomReporter(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, headroomAdvisor hmadvisor.ResourceAdvisor) (Reporter, error) {
	plugin, err := newHeadroomReporterPlugin(emitter, metaServer, conf, headroomAdvisor)
	if err != nil {
		return nil, fmt.Errorf("[headroom-reporter] create headroom reporter failed: %s", err)
	}

	return &headroomReporterImpl{plugin}, nil
}

func (r *headroomReporterImpl) Run(ctx context.Context) {
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
	allocatable v1.ResourceList
	capacity    v1.ResourceList
}

type headroomReporterPlugin struct {
	sync.Mutex
	headroomManagers map[v1.ResourceName]manager.HeadroomManager

	ctx     context.Context
	cancel  context.CancelFunc
	started bool
}

func newHeadroomReporterPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, headroomAdvisor hmadvisor.ResourceAdvisor) (skeleton.GenericPlugin, error) {
	var (
		err     error
		errList []error
	)

	initializers := manager.GetRegisteredManagerInitializers()
	headroomManagers := make(map[v1.ResourceName]manager.HeadroomManager, len(initializers))
	for name, initializer := range initializers {
		headroomManagers[name], err = initializer(emitter, metaServer, conf, headroomAdvisor)
		if err != nil {
			errList = append(errList, err)
		}
	}
	if len(errList) > 0 {
		return nil, errors.NewAggregate(errList)
	}

	reporter := &headroomReporterPlugin{
		headroomManagers: headroomManagers,
	}
	return skeleton.NewRegistrationPluginWrapper(reporter, []string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"pluginName": headroomReporterPluginName,
				"pluginType": registration.ReporterPlugin,
			})...)
		})
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
	res, err := r.getReclaimedResource()
	if err != nil {
		return nil, err
	}

	reportToCNR, err := getReportReclaimedResourceForCNR(res)
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
	for resourceName, rm := range r.headroomManagers {
		allocatable[resourceName], err = rm.GetAllocatable()
		if err != nil {
			errList = append(errList, fmt.Errorf("get reclaimed %s allocatable failed: %s", resourceName, err))
		}

		capacity[resourceName], err = rm.GetCapacity()
		if err != nil {
			errList = append(errList, err, fmt.Errorf("get reclaimed %s capacity failed: %s", resourceName, err))
		}
	}

	if len(errList) > 0 {
		return nil, errors.NewAggregate(errList)
	}

	return &reclaimedResource{
		allocatable: allocatable,
		capacity:    capacity,
	}, err
}

func getReportReclaimedResourceForCNR(reclaimedResource *reclaimedResource) (*v1alpha1.ReportContent, error) {
	if reclaimedResource == nil {
		return nil, nil
	}

	resources := nodeapis.Resources{
		Allocatable: &reclaimedResource.allocatable,
		Capacity:    &reclaimedResource.capacity,
	}

	resourcesValue, err := json.Marshal(&resources)
	if err != nil {
		return nil, err
	}

	return &v1alpha1.ReportContent{
		GroupVersionKind: &util.CNRGroupVersionKind,
		Field: []*v1alpha1.ReportField{
			{
				FieldType: v1alpha1.FieldType_Status,
				FieldName: util.CNRFieldNameResources,
				Value:     resourcesValue,
			},
		},
	}, nil
}
