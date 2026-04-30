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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/memory"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// ResourceAdvisor is a wrapper of different sub resource advisors. It can be registered to
// headroom reporter to give designated resource headroom quantity based on provision result.
type ResourceAdvisor interface {
	// Run starts all sub resource advisors
	Run(ctx context.Context)

	// GetSubAdvisor returns the corresponding sub advisor according to resource name
	GetSubAdvisor(resourceName types.QoSResourceName) (SubResourceAdvisor, error)
}

// SubResourceAdvisor updates resource provision of a certain dimension based on the latest
// system and workload snapshot(s), and returns provision advice or resource headroom quantity.
type SubResourceAdvisor interface {
	// Run starts resource provision update based on the latest system and workload snapshot(s)
	Run(ctx context.Context)

	// UpdateAndGetAdvice triggers resource provision update and returns the latest advice
	UpdateAndGetAdvice(ctx context.Context) (interface{}, error)

	// GetHeadroom returns the latest resource headroom quantity for resource reporter
	GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error)
}

type resourceAdvisorWrapper struct {
	subAdvisorsToRun map[types.QoSResourceName]SubResourceAdvisor
}

// NewResourceAdvisor returns a resource advisor wrapper instance, initializing all required
// sub resource advisor according to config
func NewResourceAdvisor(conf *config.Configuration, extraConf interface{}, metaCache metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) (ResourceAdvisor, error) {
	resourceAdvisor := resourceAdvisorWrapper{
		subAdvisorsToRun: make(map[types.QoSResourceName]SubResourceAdvisor),
	}

	for _, resourceNameStr := range conf.ResourceAdvisors {
		resourceName := types.QoSResourceName(resourceNameStr)
		subAdvisor, err := NewSubResourceAdvisor(resourceName, conf, extraConf, metaCache, metaServer, emitter)
		if err != nil {
			return nil, fmt.Errorf("new sub resource advisor for %v failed: %v", resourceName, err)
		}
		resourceAdvisor.subAdvisorsToRun[resourceName] = subAdvisor
	}

	return &resourceAdvisor, nil
}

// NewSubResourceAdvisor returns a corresponding advisor according to resource name
func NewSubResourceAdvisor(resourceName types.QoSResourceName, conf *config.Configuration, extraConf interface{},
	metaCache metacache.MetaCache, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) (SubResourceAdvisor, error) {
	switch resourceName {
	case types.QoSResourceCPU:
		return cpu.NewCPUResourceAdvisor(conf, extraConf, metaCache, metaServer, emitter), nil
	case types.QoSResourceMemory:
		return memory.NewMemoryResourceAdvisor(conf, extraConf, metaCache, metaServer, emitter), nil
	default:
		return nil, fmt.Errorf("try to new sub resource advisor for unsupported resource %v", resourceName)
	}
}

func (ra *resourceAdvisorWrapper) Run(ctx context.Context) {
	for _, subAdvisor := range ra.subAdvisorsToRun {
		go subAdvisor.Run(ctx)
	}
}

func (ra *resourceAdvisorWrapper) GetSubAdvisor(resourceName types.QoSResourceName) (SubResourceAdvisor, error) {
	if subAdvisor, ok := ra.subAdvisorsToRun[resourceName]; ok {
		return subAdvisor, nil
	}
	return nil, fmt.Errorf("no sub resource advisor for %v", resourceName)
}

func (ra *resourceAdvisorWrapper) GetHeadroom(resourceName v1.ResourceName) (resource.Quantity, map[int]resource.Quantity, error) {
	switch resourceName {
	case v1.ResourceCPU:
		return ra.getSubAdvisorHeadroom(types.QoSResourceCPU)
	case v1.ResourceMemory:
		return ra.getSubAdvisorHeadroom(types.QoSResourceMemory)
	default:
		return resource.Quantity{}, nil, fmt.Errorf("illegal resource %v", resourceName)
	}
}

func (ra *resourceAdvisorWrapper) getSubAdvisorHeadroom(resourceName types.QoSResourceName) (resource.Quantity, map[int]resource.Quantity, error) {
	subAdvisor, ok := ra.subAdvisorsToRun[resourceName]
	if !ok {
		return resource.Quantity{}, nil, fmt.Errorf("no sub resource advisor for %v", resourceName)
	}
	return subAdvisor.GetHeadroom()
}
