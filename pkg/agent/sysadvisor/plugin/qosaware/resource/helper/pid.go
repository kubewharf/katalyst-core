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

package helper

import (
	"math"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PIDController struct {
	msg                string
	variableName       string
	resourceEssentials types.ResourceEssentials
	params             types.FirstOrderPIDParams
	adjustmentTotal    float64
	controlKnobPrev    float64
	errorValue         float64
	errorValuePrev     float64
}

func NewPIDController(variableName string, params types.FirstOrderPIDParams, msg string) *PIDController {
	return &PIDController{
		msg:             msg,
		variableName:    variableName,
		params:          params,
		adjustmentTotal: 0,
		controlKnobPrev: 0,
		errorValue:      0,
		errorValuePrev:  0,
	}
}

func (c *PIDController) SetEssentials(resourceEssentials types.ResourceEssentials) {
	c.resourceEssentials = resourceEssentials
}

func (c *PIDController) Adjust(controlKnob, target, current float64, direct bool) float64 {
	var (
		kp, kd, kpSign, kdSign float64
		pterm                  float64 = 0
		dterm                  float64 = 0
		adjustment             float64 = 0
	)

	c.errorValuePrev = c.errorValue
	c.errorValue = math.Log(current) - math.Log(target)

	errorRaw := current - target
	errorRate := math.Abs(c.errorValue) - math.Abs(c.errorValuePrev)

	// apply adjustment when current is out of deadband
	if (errorRaw > 0 && errorRaw/target > c.params.DeadbandUpperPct) || (errorRaw < 0 && errorRaw/target < -c.params.DeadbandLowerPct) {
		if c.errorValue >= 0 {
			kp = c.params.Kpp
			kpSign = 1
		} else {
			kp = c.params.Kpn
			kpSign = -1
		}
		// The default value of kp is evaluated on the machine which has 96 Cores.
		// In an actual scenario, if kp remains unchanged, when the region is large, the control will be excessively sluggish.
		// So here we scale kp according to the region size.
		kp *= c.resourceEssentials.ResourceUpperBound

		if errorRate >= 0 {
			kdSign = kpSign
		} else {
			kdSign = -kpSign
		}

		if kdSign >= 0 {
			kd = c.params.Kdp
		} else {
			kd = c.params.Kdn
		}

		pterm = kp * c.errorValue
		dterm = kdSign * kd * math.Abs(errorRate)
		adjustment = pterm + dterm
	} else {
		klog.InfoS("in deadband", "errorRaw", errorRaw, "target", target, "params", c.params)
	}

	if c.controlKnobPrev != controlKnob {
		c.controlKnobPrev = controlKnob
		c.adjustmentTotal = 0
	}

	c.adjustmentTotal += adjustment
	c.adjustmentTotal = general.Clamp(c.adjustmentTotal, c.params.AdjustmentLowerBound, c.params.AdjustmentUpperBound)

	directSign := -1.0
	if !direct {
		directSign = 1
	}

	result := controlKnob + c.adjustmentTotal*directSign
	result = general.Clamp(result, c.resourceEssentials.ResourceLowerBound, c.resourceEssentials.ResourceUpperBound)

	klog.InfoS("[qosaware-cpu-pid]", "meta", c.msg, "indicator", c.variableName, "controlKnob", controlKnob,
		"adjustment", adjustment, "adjustmentTotal", c.adjustmentTotal, "result", result, "target", target, "current", current,
		"errorValue", c.errorValue, "errorRate", errorRate, "pterm", pterm, "dterm", dterm, "kp", kp, "kd", kd,
		"resourceEssentials", c.resourceEssentials, "directSign", directSign)

	return result
}
