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

package tmo

import (
	"time"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

const (
	DefaultEnableTMO                                   bool                   = false
	DefaultEnableSwap                                  bool                   = false
	DefaultTMOInterval                                 time.Duration          = 30 * time.Second
	DefaultTMOPolicyName                               v1alpha1.TMOPolicyName = v1alpha1.TMOPolicyNamePSI
	DefaultTMOMaxProbe                                 float64                = 0.01
	DefaultTMOPSIPolicyPSIAvg60Threshold               float64                = 0.1
	DefaultTMORefaultPolicyReclaimAccuracyTarget       float64                = 0.99
	DefaultTMORefaultPolicyReclaimScanEfficiencyTarget float64                = 0.6
)

type TransparentMemoryOffloadingConfiguration struct {
	DefaultConfigurations *TMODefaultConfigurations
	QoSLevelConfigs       map[consts.QoSLevel]*TMOConfigDetail
	CgroupConfigs         map[string]*TMOConfigDetail
}

func NewTransparentMemoryOffloadingConfiguration() *TransparentMemoryOffloadingConfiguration {
	return &TransparentMemoryOffloadingConfiguration{
		DefaultConfigurations: NewTMODefaultConfigurations(),
		QoSLevelConfigs:       map[consts.QoSLevel]*TMOConfigDetail{},
		CgroupConfigs:         map[string]*TMOConfigDetail{},
	}
}

type TMODefaultConfigurations struct {
	DefaultEnableTMO                                   bool
	DefaultEnableSwap                                  bool
	DefaultTMOInterval                                 time.Duration
	DefaultTMOPolicyName                               v1alpha1.TMOPolicyName
	DefaultTMOMaxProbe                                 float64
	DefaultTMOPSIPolicyPSIAvg60Threshold               float64
	DefaultTMORefaultPolicyReclaimAccuracyTarget       float64
	DefaultTMORefaultPolicyReclaimScanEfficiencyTarget float64
}

func NewTMODefaultConfigurations() *TMODefaultConfigurations {
	return &TMODefaultConfigurations{
		DefaultEnableTMO:                                   DefaultEnableTMO,
		DefaultEnableSwap:                                  DefaultEnableSwap,
		DefaultTMOInterval:                                 DefaultTMOInterval,
		DefaultTMOPolicyName:                               DefaultTMOPolicyName,
		DefaultTMOMaxProbe:                                 DefaultTMOMaxProbe,
		DefaultTMOPSIPolicyPSIAvg60Threshold:               DefaultTMOPSIPolicyPSIAvg60Threshold,
		DefaultTMORefaultPolicyReclaimAccuracyTarget:       DefaultTMORefaultPolicyReclaimAccuracyTarget,
		DefaultTMORefaultPolicyReclaimScanEfficiencyTarget: DefaultTMORefaultPolicyReclaimScanEfficiencyTarget,
	}
}

type TMOConfigDetail struct {
	EnableTMO  bool
	EnableSwap bool
	Interval   time.Duration
	PolicyName v1alpha1.TMOPolicyName
	*PSIPolicyConf
	*RefaultPolicyConf
}

func NewTMOConfigDetail(defaultConfigs *TMODefaultConfigurations) *TMOConfigDetail {
	return &TMOConfigDetail{
		EnableTMO:  defaultConfigs.DefaultEnableTMO,
		EnableSwap: defaultConfigs.DefaultEnableSwap,
		Interval:   defaultConfigs.DefaultTMOInterval,
		PolicyName: defaultConfigs.DefaultTMOPolicyName,
		PSIPolicyConf: &PSIPolicyConf{
			MaxProbe:          defaultConfigs.DefaultTMOMaxProbe,
			PsiAvg60Threshold: defaultConfigs.DefaultTMOPSIPolicyPSIAvg60Threshold,
		},
		RefaultPolicyConf: &RefaultPolicyConf{
			MaxProbe:                    defaultConfigs.DefaultTMOMaxProbe,
			ReclaimAccuracyTarget:       defaultConfigs.DefaultTMORefaultPolicyReclaimAccuracyTarget,
			ReclaimScanEfficiencyTarget: defaultConfigs.DefaultTMORefaultPolicyReclaimScanEfficiencyTarget,
		},
	}
}

type PSIPolicyConf struct {
	MaxProbe          float64
	PsiAvg60Threshold float64
}

type RefaultPolicyConf struct {
	MaxProbe                    float64
	ReclaimAccuracyTarget       float64
	ReclaimScanEfficiencyTarget float64
}

func ApplyTMOConfigDetail(tmoConfigDetail *TMOConfigDetail, tmoConfigDetailDynamic v1alpha1.TMOConfigDetail) {
	if tmoConfigDetailDynamic.EnableTMO != nil {
		tmoConfigDetail.EnableTMO = *tmoConfigDetailDynamic.EnableTMO
	}
	if tmoConfigDetailDynamic.EnableSwap != nil {
		tmoConfigDetail.EnableSwap = *tmoConfigDetailDynamic.EnableSwap
	}
	if tmoConfigDetailDynamic.Interval != nil {
		tmoConfigDetail.Interval = tmoConfigDetailDynamic.Interval.Duration
	}
	if tmoConfigDetailDynamic.PolicyName != nil {
		tmoConfigDetail.PolicyName = *tmoConfigDetailDynamic.PolicyName
	}
	if psiPolicyConfDynamic := tmoConfigDetailDynamic.PSIPolicyConf; psiPolicyConfDynamic != nil {
		if psiPolicyConfDynamic.MaxProbe != nil {
			tmoConfigDetail.PSIPolicyConf.MaxProbe = *psiPolicyConfDynamic.MaxProbe
		}
		if psiPolicyConfDynamic.PSIAvg60Threshold != nil {
			tmoConfigDetail.PSIPolicyConf.PsiAvg60Threshold = *psiPolicyConfDynamic.PSIAvg60Threshold
		}
	}
	if refaultPolicyConfDynamic := tmoConfigDetailDynamic.RefaultPolicConf; refaultPolicyConfDynamic != nil {
		if refaultPolicyConfDynamic.MaxProbe != nil {
			tmoConfigDetail.RefaultPolicyConf.MaxProbe = *refaultPolicyConfDynamic.MaxProbe
		}
		if refaultPolicyConfDynamic.ReclaimScanEfficiencyTarget != nil {
			tmoConfigDetail.RefaultPolicyConf.ReclaimAccuracyTarget = *refaultPolicyConfDynamic.ReclaimAccuracyTarget
		}
		if refaultPolicyConfDynamic.ReclaimScanEfficiencyTarget != nil {
			tmoConfigDetail.RefaultPolicyConf.ReclaimScanEfficiencyTarget = *refaultPolicyConfDynamic.ReclaimScanEfficiencyTarget
		}
	}
}

func (c *TransparentMemoryOffloadingConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if tmoConf := conf.TransparentMemoryOffloadingConfiguration; tmoConf != nil {
		if tmoConf.Spec.Config.QoSLevelConfig != nil {
			for _, qosLevelConfig := range tmoConf.Spec.Config.QoSLevelConfig {
				tmoConfigDetail := NewTMOConfigDetail(c.DefaultConfigurations)
				ApplyTMOConfigDetail(tmoConfigDetail, qosLevelConfig.ConfigDetail)
				c.QoSLevelConfigs[qosLevelConfig.QoSLevel] = tmoConfigDetail

			}
		}
		if tmoConf.Spec.Config.CgroupConfig != nil {
			for _, cgroupConfig := range tmoConf.Spec.Config.CgroupConfig {
				tmoConfigDetail := NewTMOConfigDetail(c.DefaultConfigurations)
				ApplyTMOConfigDetail(tmoConfigDetail, cgroupConfig.ConfigDetail)
				c.CgroupConfigs[cgroupConfig.CgroupPath] = tmoConfigDetail
			}
		}
	}
}
