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

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	metricRamaDominantIndicator = "rama_dominant_indicator"
	controlActingDirect         = "direct"
	controlActingReverse        = "reverse"
)

var controllerDirections = map[configapi.ControlKnobName]map[workloadv1alpha1.ServiceSystemIndicatorName]string{
	configapi.ControlKnobNonReclaimedCPURequirement: {
		workloadv1alpha1.ServiceSystemIndicatorNameCPUSchedWait:             controlActingReverse,
		workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio:            controlActingReverse,
		workloadv1alpha1.ServiceSystemIndicatorNameCPI:                      controlActingReverse,
		workloadv1alpha1.ServiceSystemIndicatorNameMemoryAccessWriteLatency: controlActingReverse,
		workloadv1alpha1.ServiceSystemIndicatorNameMemoryAccessReadLatency:  controlActingReverse,
		workloadv1alpha1.ServiceSystemIndicatorNameMemoryL3MissLatency:      controlActingReverse,
	},
	configapi.ControlKnobReclaimedCoresCPUQuota: {
		workloadv1alpha1.ServiceSystemIndicatorNameCPUSchedWait:             controlActingDirect,
		workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio:            controlActingDirect,
		workloadv1alpha1.ServiceSystemIndicatorNameCPI:                      controlActingDirect,
		workloadv1alpha1.ServiceSystemIndicatorNameMemoryAccessWriteLatency: controlActingDirect,
		workloadv1alpha1.ServiceSystemIndicatorNameMemoryAccessReadLatency:  controlActingDirect,
		workloadv1alpha1.ServiceSystemIndicatorNameMemoryL3MissLatency:      controlActingDirect,
	},
}

type PolicyRama struct {
	*PolicyBase
	conf        *config.Configuration
	controllers map[string]*helper.PIDController // map[metricName]controller
}

func NewPolicyRama(regionName string, regionType configapi.QoSRegionType, ownerPoolName string,
	conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) ProvisionPolicy {
	p := &PolicyRama{
		conf:        conf,
		PolicyBase:  NewPolicyBase(regionName, regionType, ownerPoolName, metaReader, metaServer, emitter),
		controllers: make(map[string]*helper.PIDController),
	}

	return p
}

func (p *PolicyRama) Update() error {
	// sanity check
	if err := p.sanityCheck(); err != nil {
		return err
	}

	knobName := configapi.ControlKnobNonReclaimedCPURequirement
	knobValue := 0.0
	cpuAdjustedRaw := math.Inf(-1)

	if value, ok := p.ControlKnobs[configapi.ControlKnobReclaimedCoresCPUQuota]; ok {
		knobName = configapi.ControlKnobReclaimedCoresCPUQuota
		knobValue = value.Value
		cpuAdjustedRaw = math.Inf(1)
	} else if value, ok = p.ControlKnobs[configapi.ControlKnobNonReclaimedCPURequirement]; ok {
		knobName = configapi.ControlKnobNonReclaimedCPURequirement
		knobValue = value.Value
	}

	dominantIndicator := "unknown"

	// run pid control for each indicator
	for metricName, indicator := range p.Indicators {
		params, ok := p.conf.PolicyRama.PIDParameters[metricName]
		if !ok {
			klog.Warningf("[qosaware-cpu-rama] pid parameter not found for indicator %v", metricName)
			continue
		}

		controller, ok := p.controllers[metricName]
		if !ok {
			controller = helper.NewPIDController(metricName, params, p.GetMetaInfo())
			p.controllers[metricName] = controller
		}

		direction := controlActingDirect
		if tmp, ok := controllerDirections[knobName]; ok {
			if v, ok := tmp[workloadv1alpha1.ServiceSystemIndicatorName(metricName)]; ok {
				direction = v
			}
		}

		controller.SetEssentials(p.ResourceEssentials)
		cpuAdjusted := controller.Adjust(knobValue, indicator.Target, indicator.Current, direction == controlActingDirect)

		general.InfoS("[qosaware-cpu-rama] pid adjust result", "meta", p.GetMetaInfo(), "metricName", metricName, "cpuAdjusted", cpuAdjusted, "last knobValue", knobValue, "knobName", knobName, "direction", direction, "target", indicator.Target, "current", indicator.Current, "params", params)

		if cpuAdjusted > cpuAdjustedRaw && direction == controlActingReverse {
			cpuAdjustedRaw = cpuAdjusted
			dominantIndicator = metricName
		} else if cpuAdjusted < cpuAdjustedRaw && direction == controlActingDirect {
			cpuAdjustedRaw = cpuAdjusted
			dominantIndicator = metricName
		}
	}

	period := p.conf.QoSAwarePluginConfiguration.SyncPeriod
	_ = p.emitter.StoreInt64(metricRamaDominantIndicator, int64(period.Seconds()), metrics.MetricTypeNameCount, []metrics.MetricTag{
		{Key: "metric_name", Val: dominantIndicator},
	}...)

	for metricName := range p.controllers {
		_, ok := p.conf.PolicyRama.PIDParameters[metricName]
		if !ok {
			delete(p.controllers, metricName)
		}
	}

	general.Infof("rama update ret: %s, %v", knobName, cpuAdjustedRaw)

	cpuAdjustedRestricted := cpuAdjustedRaw

	p.controlKnobAdjusted = types.ControlKnob{
		knobName: types.ControlKnobItem{
			Value:  cpuAdjustedRestricted,
			Action: types.ControlKnobActionNone,
		},
	}

	return nil
}

func (p *PolicyRama) sanityCheck() error {
	var (
		isLegal bool
		errList []error
	)

	enableReclaim := p.conf.GetDynamicConfiguration().EnableReclaim

	// 1. check if enable reclaim
	if !enableReclaim {
		errList = append(errList, fmt.Errorf("reclaim disabled"))
	}

	// 2. check margin. skip update when margin is non zero
	if p.ResourceEssentials.ReservedForAllocate != 0 {
		errList = append(errList, fmt.Errorf("margin exists"))
	}

	// 3. check control knob legality
	isLegal = true
	if p.ControlKnobs == nil || len(p.ControlKnobs) <= 0 {
		isLegal = false
	} else {
		v1, ok1 := p.ControlKnobs[configapi.ControlKnobNonReclaimedCPURequirement]
		v2, ok2 := p.ControlKnobs[configapi.ControlKnobReclaimedCoresCPUQuota]

		if !ok1 && !ok2 {
			isLegal = false
		} else if ok1 && v1.Value < 0 {
			isLegal = false
		} else if ok2 && v2.Value <= 0 {
			isLegal = false
		}
	}
	if !isLegal {
		errList = append(errList, fmt.Errorf("illegal control knob %v", p.ControlKnobs))
	}

	// 4. check indicators legality
	if p.Indicators == nil {
		errList = append(errList, fmt.Errorf("illegal indicators"))
	}

	return errors.NewAggregate(errList)
}
