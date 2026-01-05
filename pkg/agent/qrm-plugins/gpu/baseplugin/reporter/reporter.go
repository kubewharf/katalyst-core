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
	"k8s.io/apimachinery/pkg/api/resource"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	gpuReporterPluginName   = "gpu-reporter-plugin"
	propertyNameGPUTopology = "gpu_topology_attribute_key"
)

var oneQuantity = *resource.NewQuantity(1, resource.DecimalSI)

// gpuReporterPlugin is the plugin that reports gpu device topology information
type gpuReporterPlugin struct {
	sync.Mutex

	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	emitter metrics.MetricEmitter

	numaSocketZoneNodeMap  map[util.ZoneNode]util.ZoneNode
	deviceTopologyRegistry *machine.DeviceTopologyRegistry
}

var (
	_ skeleton.GenericPlugin        = (*gpuReporterPlugin)(nil)
	_ v1alpha1.ReporterPluginServer = (*gpuReporterPlugin)(nil)
)

func NewGPUReporterPlugin(emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	conf *config.Configuration, topologyRegistry *machine.DeviceTopologyRegistry,
) (skeleton.GenericPlugin, v1alpha1.ReporterPluginServer, error) {
	reporter := &gpuReporterPlugin{
		numaSocketZoneNodeMap:  util.GenerateNumaSocketZone(metaServer.MachineInfo.Topology),
		emitter:                emitter,
		deviceTopologyRegistry: topologyRegistry,
	}
	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(reporter, []string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"pluginName": gpuReporterPluginName,
				"pluginType": registration.ReporterPlugin,
			})...)
		})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to register %s plugin: %w", gpuReporterPluginName, err)
	}

	return pluginWrapper, reporter, nil
}

func (p *gpuReporterPlugin) Name() string {
	return gpuReporterPluginName
}

func (p *gpuReporterPlugin) Start() (err error) {
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

	p.ctx, p.cancel = context.WithCancel(context.Background())
	return
}

func (p *gpuReporterPlugin) Stop() error {
	p.Lock()
	defer func() {
		p.started = false
		p.Unlock()
	}()

	if !p.started {
		return nil
	}

	p.cancel()
	return nil
}

// GetReportContent implements ReporterPluginServer to report the gpu device topology information to CNR.
func (p *gpuReporterPlugin) GetReportContent(_ context.Context, _ *v1alpha1.Empty) (*v1alpha1.GetReportContentResponse, error) {
	// device topology is not updated yet, report the topology next time
	deviceTopology, ready, err := p.deviceTopologyRegistry.GetDeviceTopology(gpuconsts.GPUDeviceType)
	if err != nil {
		return nil, fmt.Errorf("failed to get device topology: %w", err)
	}
	if !ready {
		general.Infof("device topology is not ready yet")
		return nil, fmt.Errorf("device topology is not ready yet")
	}

	resourceProperty := p.getGPUResourceProperty(deviceTopology)

	// generate the zones for numa and socket in machine
	topologyZoneGenerator, err := util.NewNumaSocketTopologyZoneGenerator(p.numaSocketZoneNodeMap)
	if err != nil {
		return nil, fmt.Errorf("failed to create topology zone generator: %w", err)
	}

	// add the GPU zone nodes in and generate their topology zones by merging their resources and attributes
	if err = p.addGPUZoneNodes(deviceTopology, topologyZoneGenerator); err != nil {
		return nil, err
	}

	generatedTopologyZones := topologyZoneGenerator.GenerateTopologyZoneStatus(nil, p.getZoneResources(deviceTopology),
		p.getGPUZoneAttributes(deviceTopology), nil, nil)

	propertyValues, err := json.Marshal(&resourceProperty)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal resource property values: %w", err)
	}

	zoneValues, err := json.Marshal(&generatedTopologyZones)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal topology zone values: %w", err)
	}

	return &v1alpha1.GetReportContentResponse{
		Content: []*v1alpha1.ReportContent{
			{
				GroupVersionKind: &util.CNRGroupVersionKind,
				Field: []*v1alpha1.ReportField{
					{
						FieldType: v1alpha1.FieldType_Spec,
						FieldName: util.CNRFieldNameNodeResourceProperties,
						Value:     propertyValues,
					},
					{
						FieldType: v1alpha1.FieldType_Status,
						FieldName: util.CNRFieldNameTopologyZone,
						Value:     zoneValues,
					},
				},
			},
		},
	}, nil
}

// getGPUResourceProperty returns the different dimensions to differentiate affinity priority of gpu devices.
func (p *gpuReporterPlugin) getGPUResourceProperty(deviceTopology *machine.DeviceTopology) []*nodev1alpha1.Property {
	if deviceTopology == nil {
		return nil
	}

	return []*nodev1alpha1.Property{
		{
			PropertyName:   propertyNameGPUTopology,
			PropertyValues: deviceTopology.PriorityDimensions,
		},
	}
}

// getGPUZoneAttributes returns the map of gpu zone nodes to their attributes
func (p *gpuReporterPlugin) getGPUZoneAttributes(deviceTopology *machine.DeviceTopology) map[util.ZoneNode]util.ZoneAttributes {
	if deviceTopology == nil {
		return nil
	}

	zoneAttributes := make(map[util.ZoneNode]util.ZoneAttributes)

	for id, device := range deviceTopology.Devices {
		dimensions := device.GetDimensions()
		zoneNode := util.GenerateDeviceZoneNode(id, string(nodev1alpha1.TopologyTypeGPU))

		var attributes []nodev1alpha1.Attribute
		for _, dimension := range dimensions {
			attributes = append(attributes, nodev1alpha1.Attribute{
				Name:  dimension.GetName(),
				Value: dimension.GetValue(),
			})
		}

		zoneAttributes[zoneNode] = attributes
	}

	return zoneAttributes
}

// addGPUZoneNodes adds the gpu zone nodes to the topology zone generator
func (p *gpuReporterPlugin) addGPUZoneNodes(deviceTopology *machine.DeviceTopology, generator *util.TopologyZoneGenerator) error {
	if deviceTopology == nil {
		return nil
	}

	var errList []error

	for id, device := range deviceTopology.Devices {
		deviceNode := util.GenerateDeviceZoneNode(id, string(nodev1alpha1.TopologyTypeGPU))
		for _, numaNode := range device.NumaNodes {
			numaZoneNode := util.GenerateNumaZoneNode(numaNode)
			err := generator.AddNode(&numaZoneNode, deviceNode)
			if err != nil {
				errList = append(errList, err)
			}
		}
	}

	return utilerrors.NewAggregate(errList)
}

// getZoneResources returns the map of gpu zone nodes to their resources
func (p *gpuReporterPlugin) getZoneResources(deviceTopology *machine.DeviceTopology) map[util.ZoneNode]nodev1alpha1.Resources {
	if deviceTopology == nil {
		return nil
	}

	zoneResources := make(map[util.ZoneNode]nodev1alpha1.Resources)
	deviceName := deviceTopology.DeviceName
	for id := range deviceTopology.Devices {
		zoneNode := util.GenerateDeviceZoneNode(id, string(nodev1alpha1.TopologyTypeGPU))

		resources := nodev1alpha1.Resources{
			Allocatable: &v1.ResourceList{
				deviceName: oneQuantity,
			},
			Capacity: &v1.ResourceList{
				deviceName: oneQuantity,
			},
		}

		zoneResources[zoneNode] = resources
	}

	return zoneResources
}

// ListAndWatchReportContent implements ReporterPluginServer to list and watch report content.
func (p *gpuReporterPlugin) ListAndWatchReportContent(_ *v1alpha1.Empty, server v1alpha1.ReporterPlugin_ListAndWatchReportContentServer) error {
	for {
		select {
		case <-p.ctx.Done():
			return nil
		case <-server.Context().Done():
			return nil
		}
	}
}
