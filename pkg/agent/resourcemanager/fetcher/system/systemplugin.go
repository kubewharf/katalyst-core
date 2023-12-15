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

package system

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/plugin"
	pluginutil "github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	PluginName = "system-reporter-plugin"

	ResourceNameNBW v1.ResourceName = "nbw"

	PropertyNameCIS      = "cis"
	PropertyNameNUMA     = "numa"
	PropertyNameTopology = "topology"
)

// systemPlugin implements the endpoint interface, and it's an in-tree reporter plugin
type systemPlugin struct {
	// conf is used to indicate the file path and name for system data in the future
	// currently, it's not used todo: implement this logic
	conf *config.Configuration

	mutex                       sync.Mutex
	latestReportContentResponse *v1alpha1.GetReportContentResponse

	*process.StopControl
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
}

func NewSystemReporterPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, _ plugin.ListAndWatchCallback) (plugin.ReporterPlugin, error) {
	p := &systemPlugin{
		conf:        conf,
		emitter:     emitter,
		metaServer:  metaServer,
		StopControl: process.NewStopControl(time.Time{}),
	}

	return p, nil
}

func (p *systemPlugin) Name() string {
	return PluginName
}

func (p *systemPlugin) Run(success chan<- bool) {
	success <- true
	select {}
}

func (p *systemPlugin) GetReportContent(_ context.Context) (*v1alpha1.GetReportContentResponse, error) {
	content, err := pluginutil.AppendReportContent(
		p.getResourceProperties,
	)
	if err != nil {
		return nil, err
	}

	resp := &v1alpha1.GetReportContentResponse{
		Content: content,
	}

	p.setCache(resp)

	return resp, nil
}

func (p *systemPlugin) ListAndWatchReportContentCallback(_ string, _ *v1alpha1.GetReportContentResponse) {
}

func (p *systemPlugin) GetCache() *v1alpha1.GetReportContentResponse {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	return p.latestReportContentResponse
}

func (p *systemPlugin) setCache(resp *v1alpha1.GetReportContentResponse) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	p.latestReportContentResponse = resp
}

func (p *systemPlugin) getResourceProperties() ([]*v1alpha1.ReportContent, error) {
	var properties []*nodev1alpha1.Property

	// append all properties to one property list
	properties = append(properties,
		p.getNUMACount(),
		p.getNetworkBandwidth(),
		p.getCPUCount(),
		p.getMemoryCapacity(),
		p.getCISProperty(),
		p.getNetworkTopologyProperty(),
	)

	value, err := json.Marshal(&properties)
	if err != nil {
		return nil, errors.Wrap(err, "marshal resource properties failed")
	}

	return []*v1alpha1.ReportContent{
		{
			GroupVersionKind: &util.CNRGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: util.CNRFieldNameNodeResourceProperties,
					Value:     value,
				},
			},
		},
	}, nil
}

// getNUMACount get numa count of this machine.
func (p *systemPlugin) getNUMACount() *nodev1alpha1.Property {
	return &nodev1alpha1.Property{
		PropertyName:     PropertyNameNUMA,
		PropertyQuantity: resource.NewQuantity(int64(p.metaServer.CPUTopology.NumNUMANodes), resource.DecimalSI),
	}
}

// getNetworkBandwidth get max network bandwidth of all the interfaces in this machine.
func (p *systemPlugin) getNetworkBandwidth() *nodev1alpha1.Property {
	// check all interface, save max speed of all enabled interfaces
	max := -1
	for _, net := range p.metaServer.ExtraNetworkInfo.Interface {
		if net.Enable && net.Speed > max {
			max = net.Speed
		}
	}

	return &nodev1alpha1.Property{
		PropertyName:     fmt.Sprintf("%v", ResourceNameNBW),
		PropertyQuantity: resource.NewQuantity(int64(max), resource.DecimalSI),
	}
}

// getCPUCount get cpu count of this machine.
func (p *systemPlugin) getCPUCount() *nodev1alpha1.Property {
	return &nodev1alpha1.Property{
		PropertyName:     fmt.Sprintf("%v", v1.ResourceCPU),
		PropertyQuantity: resource.NewQuantity(int64(p.metaServer.MachineInfo.NumCores), resource.DecimalSI),
	}
}

// getMemoryCapacity get memory capacity of this machine.
func (p *systemPlugin) getMemoryCapacity() *nodev1alpha1.Property {
	return &nodev1alpha1.Property{
		PropertyName:     fmt.Sprintf("%v", v1.ResourceMemory),
		PropertyQuantity: resource.NewQuantity(int64(p.metaServer.MachineInfo.MemoryCapacity), resource.BinarySI),
	}
}

func (p *systemPlugin) getCISProperty() *nodev1alpha1.Property {
	return &nodev1alpha1.Property{
		PropertyName:   PropertyNameCIS,
		PropertyValues: p.metaServer.SupportInstructionSet.List(),
	}
}

// getNetworkTopologyProperty get network interface info of each interface in this machine.
func (p *systemPlugin) getNetworkTopologyProperty() *nodev1alpha1.Property {
	propertyValues := make([]string, 0, len(p.metaServer.ExtraNetworkInfo.Interface))

	// construct property values for each interface, each interface with
	// one property value
	for _, net := range p.metaServer.ExtraNetworkInfo.Interface {
		netBytes, err := json.Marshal(net)
		if err != nil {
			klog.Warningf("marshal network info failed: %s", err)
			return nil
		}

		propertyValues = append(propertyValues, string(netBytes))
	}

	return &nodev1alpha1.Property{
		PropertyName:   PropertyNameTopology,
		PropertyValues: propertyValues,
	}
}
