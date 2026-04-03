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

package userwatermark

import (
	"time"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

const (
	DefaultReconcileInterval      = 30
	DefaultReclaimInterval        = 10
	DefaultBackoffDuration        = 3 * time.Second
	DefaultSingleReclaimSize      = 1 * 1024 * 1024 * 1024
	DefaultScaleFactor            = 100
	DefaultSingleReclaimFactor    = 0.5
	DefaultReclaimFailedThreshold = 3
	DefaultFailureFreezePeriod    = 5 * time.Second

	DefaultPSIPolicyPSIAvg60Threshold               float64 = 0.1
	DefaultRefaultPolicyReclaimAccuracyTarget       float64 = 0.99
	DefaultRefaultPolicyReclaimScanEfficiencyTarget float64 = 0.6
)

type UserWatermarkDefaultConfiguration struct {
	EnableMemoryReclaim bool
	ReclaimInterval     int64

	ScaleFactor         uint64
	SingleReclaimFactor float64
	// SingleReclaimSize is the max memory reclaim size in one reclaim cycle
	SingleReclaimSize uint64

	BackoffDuration        time.Duration
	FeedbackPolicy         v1alpha1.UserWatermarkPolicyName
	ReclaimFailedThreshold uint64
	FailureFreezePeriod    time.Duration

	PsiAvg60Threshold           float64
	ReclaimAccuracyTarget       float64
	ReclaimScanEfficiencyTarget float64
}

func NewUserWatermarkDefaultConfiguration() *UserWatermarkDefaultConfiguration {
	return &UserWatermarkDefaultConfiguration{
		EnableMemoryReclaim:         true,
		ReclaimInterval:             DefaultReclaimInterval,
		ScaleFactor:                 DefaultScaleFactor,
		SingleReclaimFactor:         DefaultSingleReclaimFactor,
		SingleReclaimSize:           DefaultSingleReclaimSize,
		BackoffDuration:             DefaultBackoffDuration,
		FeedbackPolicy:              v1alpha1.UserWatermarkPolicyNameIntegrated,
		ReclaimFailedThreshold:      DefaultReclaimFailedThreshold,
		FailureFreezePeriod:         DefaultFailureFreezePeriod,
		PsiAvg60Threshold:           DefaultPSIPolicyPSIAvg60Threshold,
		ReclaimAccuracyTarget:       DefaultRefaultPolicyReclaimAccuracyTarget,
		ReclaimScanEfficiencyTarget: DefaultRefaultPolicyReclaimScanEfficiencyTarget,
	}
}

type PSIPolicyConf struct {
	PsiAvg60Threshold float64
}

type RefaultPolicyConf struct {
	ReclaimAccuracyTarget       float64
	ReclaimScanEfficiencyTarget float64
}

type ReclaimConfigDetail struct {
	EnableMemoryReclaim bool
	ReclaimInterval     int64

	ScaleFactor         uint64
	SingleReclaimFactor float64
	// SingleReclaimSize is the max memory reclaim size in one reclaim cycle
	SingleReclaimSize uint64

	BackoffDuration time.Duration
	FeedbackPolicy  v1alpha1.UserWatermarkPolicyName

	ReclaimFailedThreshold uint64
	FailureFreezePeriod    time.Duration

	*PSIPolicyConf
	*RefaultPolicyConf
}

func NewReclaimConfigDetail(defaultConfigs *UserWatermarkDefaultConfiguration) *ReclaimConfigDetail {
	detail := &ReclaimConfigDetail{
		EnableMemoryReclaim: defaultConfigs.EnableMemoryReclaim,
		ReclaimInterval:     defaultConfigs.ReclaimInterval,
		ScaleFactor:         defaultConfigs.ScaleFactor,
		SingleReclaimFactor: defaultConfigs.SingleReclaimFactor,
		SingleReclaimSize:   defaultConfigs.SingleReclaimSize,

		BackoffDuration:        defaultConfigs.BackoffDuration,
		FeedbackPolicy:         defaultConfigs.FeedbackPolicy,
		ReclaimFailedThreshold: defaultConfigs.ReclaimFailedThreshold,
		FailureFreezePeriod:    defaultConfigs.FailureFreezePeriod,
		PSIPolicyConf:          &PSIPolicyConf{},
		RefaultPolicyConf:      &RefaultPolicyConf{},
	}
	detail.PSIPolicyConf.PsiAvg60Threshold = defaultConfigs.PsiAvg60Threshold
	detail.RefaultPolicyConf.ReclaimAccuracyTarget = defaultConfigs.ReclaimAccuracyTarget
	detail.RefaultPolicyConf.ReclaimScanEfficiencyTarget = defaultConfigs.ReclaimScanEfficiencyTarget

	return detail
}

type UserWatermarkConfiguration struct {
	EnableReclaimer   bool
	ReconcileInterval int64
	ServiceLabel      string
	DefaultConfig     *UserWatermarkDefaultConfiguration
	ServiceConfig     map[string]*ReclaimConfigDetail
	QoSLevelConfig    map[consts.QoSLevel]*ReclaimConfigDetail
	CgroupConfig      map[string]*ReclaimConfigDetail
}

func NewUserWatermarkConfiguration() *UserWatermarkConfiguration {
	return &UserWatermarkConfiguration{
		EnableReclaimer:   false,
		ReconcileInterval: DefaultReconcileInterval,
		DefaultConfig:     NewUserWatermarkDefaultConfiguration(),
		ServiceConfig:     map[string]*ReclaimConfigDetail{},
		QoSLevelConfig:    map[consts.QoSLevel]*ReclaimConfigDetail{},
		CgroupConfig:      map[string]*ReclaimConfigDetail{},
	}
}

func ApplyReclaimConfigDetail(detail *ReclaimConfigDetail, configDetail v1alpha1.ReclaimConfigDetail) {
	if configDetail.EnableMemoryReclaim != nil {
		detail.EnableMemoryReclaim = *configDetail.EnableMemoryReclaim
	}
	if configDetail.ReclaimInterval != nil {
		detail.ReclaimInterval = *configDetail.ReclaimInterval
	}
	if configDetail.ScaleFactor != nil {
		detail.ScaleFactor = *configDetail.ScaleFactor
	}
	if configDetail.SingleReclaimFactor != nil {
		detail.SingleReclaimFactor = *configDetail.SingleReclaimFactor
	}
	if configDetail.SingleReclaimSize != nil {
		detail.SingleReclaimSize = *configDetail.SingleReclaimSize
	}
	if configDetail.BackoffDuration != nil {
		detail.BackoffDuration = configDetail.BackoffDuration.Duration
	}
	if configDetail.ReclaimFailedThreshold != nil {
		detail.ReclaimFailedThreshold = *configDetail.ReclaimFailedThreshold
	}
	if configDetail.FailureFreezePeriod != nil {
		detail.FailureFreezePeriod = configDetail.FailureFreezePeriod.Duration
	}

	if psiPolicyConfDynamic := configDetail.PSIPolicyConf; psiPolicyConfDynamic != nil {
		if psiPolicyConfDynamic.PSIAvg60Threshold != nil {
			detail.PSIPolicyConf.PsiAvg60Threshold = *psiPolicyConfDynamic.PSIAvg60Threshold
		}
	}
	if refaultPolicyConfDynamic := configDetail.RefaultPolicConf; refaultPolicyConfDynamic != nil {
		if refaultPolicyConfDynamic.ReclaimAccuracyTarget != nil {
			detail.RefaultPolicyConf.ReclaimAccuracyTarget = *refaultPolicyConfDynamic.ReclaimAccuracyTarget
		}
		if refaultPolicyConfDynamic.ReclaimScanEfficiencyTarget != nil {
			detail.RefaultPolicyConf.ReclaimScanEfficiencyTarget = *refaultPolicyConfDynamic.ReclaimScanEfficiencyTarget
		}
	}
}

func (c *UserWatermarkConfiguration) ApplyConfiguration(conf *crd.DynamicConfigCRD) {
	if uwc := conf.UserWatermarkConfiguration; uwc != nil {
		if uwc.Spec.Config.EnableReclaimer != nil {
			c.EnableReclaimer = *uwc.Spec.Config.EnableReclaimer
		}
		if uwc.Spec.Config.ReconcileInterval != nil {
			c.ReconcileInterval = *uwc.Spec.Config.ReconcileInterval
		}
		if len(uwc.Spec.Config.ServiceLabel) > 0 {
			c.ServiceLabel = uwc.Spec.Config.ServiceLabel
		}

		if uwc.Spec.Config.DefaultConfig != nil {
			if uwc.Spec.Config.DefaultConfig.EnableMemoryReclaim != nil {
				c.DefaultConfig.EnableMemoryReclaim = *uwc.Spec.Config.DefaultConfig.EnableMemoryReclaim
			}
			if uwc.Spec.Config.DefaultConfig.ReclaimInterval != nil {
				c.DefaultConfig.ReclaimInterval = *uwc.Spec.Config.DefaultConfig.ReclaimInterval
			}
			if uwc.Spec.Config.DefaultConfig.ScaleFactor != nil {
				c.DefaultConfig.ScaleFactor = *uwc.Spec.Config.DefaultConfig.ScaleFactor
			}
			if uwc.Spec.Config.DefaultConfig.SingleReclaimFactor != nil {
				c.DefaultConfig.SingleReclaimFactor = *uwc.Spec.Config.DefaultConfig.SingleReclaimFactor
			}
			if uwc.Spec.Config.DefaultConfig.SingleReclaimSize != nil {
				c.DefaultConfig.SingleReclaimSize = *uwc.Spec.Config.DefaultConfig.SingleReclaimSize
			}
			if uwc.Spec.Config.DefaultConfig.BackoffDuration != nil {
				c.DefaultConfig.BackoffDuration = uwc.Spec.Config.DefaultConfig.BackoffDuration.Duration
			}
			if uwc.Spec.Config.DefaultConfig.ReclaimFailedThreshold != nil {
				c.DefaultConfig.ReclaimFailedThreshold = *uwc.Spec.Config.DefaultConfig.ReclaimFailedThreshold
			}
			if uwc.Spec.Config.DefaultConfig.FailureFreezePeriod != nil {
				c.DefaultConfig.FailureFreezePeriod = uwc.Spec.Config.DefaultConfig.FailureFreezePeriod.Duration
			}
			if psiConf := uwc.Spec.Config.DefaultConfig.PSIPolicyConf; psiConf != nil {
				if psiConf.PSIAvg60Threshold != nil {
					c.DefaultConfig.PsiAvg60Threshold = *psiConf.PSIAvg60Threshold
				}
			}
			if refaultConf := uwc.Spec.Config.DefaultConfig.RefaultPolicConf; refaultConf != nil {
				if refaultConf.ReclaimAccuracyTarget != nil {
					c.DefaultConfig.ReclaimAccuracyTarget = *refaultConf.ReclaimAccuracyTarget
				}
				if refaultConf.ReclaimScanEfficiencyTarget != nil {
					c.DefaultConfig.ReclaimScanEfficiencyTarget = *refaultConf.ReclaimScanEfficiencyTarget
				}
			}
		}

		if uwc.Spec.Config.ServiceConfig != nil {
			for _, serviceConfig := range uwc.Spec.Config.ServiceConfig {
				configDetail := NewReclaimConfigDetail(c.DefaultConfig)
				ApplyReclaimConfigDetail(configDetail, serviceConfig.ConfigDetail)
				c.ServiceConfig[serviceConfig.ServiceName] = configDetail
			}
		}
		if uwc.Spec.Config.QoSLevelConfig != nil {
			for _, qosLevelConfig := range uwc.Spec.Config.QoSLevelConfig {
				configDetail := NewReclaimConfigDetail(c.DefaultConfig)
				ApplyReclaimConfigDetail(configDetail, qosLevelConfig.ConfigDetail)
				c.QoSLevelConfig[qosLevelConfig.QoSLevel] = configDetail
			}
		}

		if uwc.Spec.Config.CgroupConfig != nil {
			for _, cgroupConfig := range uwc.Spec.Config.CgroupConfig {
				configDetail := NewReclaimConfigDetail(c.DefaultConfig)
				ApplyReclaimConfigDetail(configDetail, cgroupConfig.ConfigDetail)
				c.CgroupConfig[cgroupConfig.CgroupPath] = configDetail
			}
		}
	}
}
