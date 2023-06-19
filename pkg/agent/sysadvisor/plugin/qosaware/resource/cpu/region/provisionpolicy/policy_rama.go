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

package provisionpolicy

import (
	"fmt"
	"math"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

type PolicyRama struct {
	*PolicyBase

	configuredIndicatorMetrics []string
	controllers                map[string]helper.PIDController // map[metricName]controller
}

func NewPolicyRama(regionName string, regionType types.QoSRegionType, ownerPoolName string,
	conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) ProvisionPolicy {
	p := &PolicyRama{
		PolicyBase:                 NewPolicyBase(regionName, regionType, ownerPoolName, metaReader, metaServer, emitter),
		configuredIndicatorMetrics: []string{},
		controllers:                make(map[string]helper.PIDController),
	}

	// initialize indicator metrics region interested in
	indicatorMetrics, ok := conf.PolicyRama.IndicatorMetrics[regionType]
	if !ok {
		klog.Warningf("[qosaware-cpu-rama] indicator metrics not found for region %v", regionType)
		return p
	}
	p.configuredIndicatorMetrics = indicatorMetrics

	// initialize pid controllers for every indicator of this region
	for _, metricName := range p.configuredIndicatorMetrics {
		params, ok := conf.PolicyRama.PIDParameters[metricName]
		if !ok {
			klog.Warningf("[qosaware-cpu-rama] pid parameter not found for indicator %v", metricName)
			continue
		}
		p.controllers[metricName] = helper.NewPIDController(params)
	}

	return p
}

func (p *PolicyRama) Update() error {
	// sanity check
	if err := p.sanityCheck(); err != nil {
		return err
	}

	cpuSize := p.ControlKnobs[types.ControlKnobNonReclaimedCPUSize].Value

	// pass the current value to regulator to avoid slow start from zero after restart
	p.regulator.SetLatestCPURequirement(int(cpuSize))

	cpuSizeAdjustedGlobal := math.Inf(-1)

	// run pid control for each indicator
	for _, metricName := range p.configuredIndicatorMetrics {
		indicator := p.Indicators[metricName]
		controller := p.controllers[metricName]

		cpuSizeAdjusted := controller.Adjust(cpuSize, indicator.Target, indicator.Current)
		if cpuSizeAdjusted > cpuSizeAdjustedGlobal {
			cpuSizeAdjustedGlobal = cpuSizeAdjusted
		}
	}
	p.regulator.Regulate(cpuSizeAdjustedGlobal)

	return nil
}

func (p *PolicyRama) sanityCheck() error {
	var (
		isLegal bool
		errList []error
	)

	// 1. check control knob legality
	isLegal = true
	if p.ControlKnobs == nil || len(p.ControlKnobs) <= 0 {
		isLegal = false
	} else {
		v, ok := p.ControlKnobs[types.ControlKnobNonReclaimedCPUSize]
		if !ok || v.Value <= 0 {
			isLegal = false
		}
	}
	if !isLegal {
		errList = append(errList, fmt.Errorf("illegal control knob %v", p.ControlKnobs))
	}

	// 2. check indicator legality
	isLegal = true
	for _, metricName := range p.configuredIndicatorMetrics {
		indicator, ok := p.Indicators[metricName]
		if !ok {
			isLegal = false
		} else if indicator.Target <= 0 || indicator.Current <= 0 {
			isLegal = false
		}
	}
	if !isLegal {
		errList = append(errList, fmt.Errorf("illegal indicator %v", p.Indicators))
	}

	// 3. check pid controllers
	for _, metricName := range p.configuredIndicatorMetrics {
		if _, ok := p.controllers[metricName]; !ok {
			errList = append(errList, fmt.Errorf("missing controller for indicator %v", metricName))
		}
	}

	// 4. check margin. skip update when margin is non zero
	if p.ResourceEssentials.ReservedForAllocate != 0 {
		errList = append(errList, fmt.Errorf("margin exists"))
	}

	return errors.NewAggregate(errList)
}
