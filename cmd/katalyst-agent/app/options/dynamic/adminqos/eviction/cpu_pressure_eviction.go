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

package eviction

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

const (
	defaultEnableLoadEviction              = false
	defaultLoadUpperBoundRatio             = 1.8
	defaultLoadLowerBoundRatio             = 1.0
	defaultLoadThresholdMetPercentage      = 0.8
	defaultLoadMetricSize                  = 10
	defaultLoadEvictionCoolDownTime        = 300 * time.Second
	defaultEnableSuppressionEviction       = false
	defaultMaxSuppressionToleranceRate     = 5
	defaultMinSuppressionToleranceDuration = 300 * time.Second
	defaultGracePeriod                     = -1
)

// CPUPressureEvictionOptions is the options of cpu pressure eviction
type CPUPressureEvictionOptions struct {
	EnableLoadEviction                bool
	LoadUpperBoundRatio               float64
	LoadLowerBoundRatio               float64
	LoadThresholdMetPercentage        float64
	LoadMetricRingSize                int
	LoadEvictionCoolDownTime          time.Duration
	EnableSuppressionEviction         bool
	MaxSuppressionToleranceRate       float64
	MinSuppressionToleranceDuration   time.Duration
	GracePeriod                       int64
	NumaCPUPressureEvictionOptions    NumaCPUPressureEvictionOptions
	NumaSysCPUPressureEvictionOptions NumaSysCPUPressureEvictionOptions
}

// NewCPUPressureEvictionOptions returns a new CPUPressureEvictionOptions
func NewCPUPressureEvictionOptions() *CPUPressureEvictionOptions {
	return &CPUPressureEvictionOptions{
		EnableLoadEviction:                defaultEnableLoadEviction,
		LoadUpperBoundRatio:               defaultLoadUpperBoundRatio,
		LoadLowerBoundRatio:               defaultLoadLowerBoundRatio,
		LoadThresholdMetPercentage:        defaultLoadThresholdMetPercentage,
		LoadMetricRingSize:                defaultLoadMetricSize,
		LoadEvictionCoolDownTime:          defaultLoadEvictionCoolDownTime,
		EnableSuppressionEviction:         defaultEnableSuppressionEviction,
		MaxSuppressionToleranceRate:       defaultMaxSuppressionToleranceRate,
		MinSuppressionToleranceDuration:   defaultMinSuppressionToleranceDuration,
		GracePeriod:                       defaultGracePeriod,
		NumaCPUPressureEvictionOptions:    NewNumaCPUPressureEvictionOptions(),
		NumaSysCPUPressureEvictionOptions: NewNumaSysCPUPressureEvictionOptions(),
	}
}

// AddFlags parses the flags to CPUPressureEvictionOptions
func (o *CPUPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-cpu-pressure")

	fs.BoolVar(&o.EnableLoadEviction, "eviction-load-enable", o.EnableLoadEviction,
		"set true to enable cpu load eviction")
	fs.Float64Var(&o.LoadUpperBoundRatio, "eviction-load-upper-bound-ratio", o.LoadUpperBoundRatio,
		"multiply the target cpuset pool size by this ratio to get the load upper bound. "+
			"if the load of the target cpuset pool is greater than the load upper bound repeatedly, the eviction will be triggered")
	fs.Float64Var(&o.LoadLowerBoundRatio, "eviction-load-lower-bound-ratio", o.LoadLowerBoundRatio,
		"multiply the target cpuset pool size by this ratio to get the load lower bound. "+
			"if the load of the target cpuset pool is greater than the load lower bound repeatedly, the node taint will be triggered")
	fs.Float64Var(&o.LoadThresholdMetPercentage, "eviction-load-threshold-met-percentage", o.LoadThresholdMetPercentage,
		"the ratio between the times metric value over the bound value and the metric ring size is greater than this percentage "+
			", the eviction or node taint will be triggered")
	fs.IntVar(&o.LoadMetricRingSize, "eviction-load-metric-ring-size", o.LoadMetricRingSize,
		"the size of the metric ring, which is used to calculate the load of the target cpuset pool")
	fs.DurationVar(&o.LoadEvictionCoolDownTime, "eviction-load-cool-down-time", o.LoadEvictionCoolDownTime,
		"the cool-down time of cpu pressure eviction, if the cpu pressure eviction is triggered, "+
			"the cpu pressure eviction will be disabled for the cool-down time")
	fs.BoolVar(&o.EnableSuppressionEviction, "eviction-suppression-enable", o.EnableSuppressionEviction,
		"whether to enable pod-level cpu suppression eviction")
	fs.Float64Var(&o.MaxSuppressionToleranceRate, "eviction-suppression-max-tolerance-rate", o.MaxSuppressionToleranceRate,
		"the maximum cpu suppression tolerance rate that can be set by the pod")
	fs.DurationVar(&o.MinSuppressionToleranceDuration, "eviction-suppression-min-tolerance-duration", o.MinSuppressionToleranceDuration,
		"the minimum duration a pod can tolerate cpu suppression")
	fs.Int64Var(&o.GracePeriod, "eviction-cpu-grace-period", o.GracePeriod,
		"the ratio between the times metric value over the bound value and the metric ring size is greater than this percentage "+
			", the eviction or node taint will be triggered")
	o.NumaCPUPressureEvictionOptions.AddFlags(fss)
	o.NumaSysCPUPressureEvictionOptions.AddFlags(fss)
}

func (o *CPUPressureEvictionOptions) ApplyTo(c *eviction.CPUPressureEvictionConfiguration) error {
	c.EnableLoadEviction = o.EnableLoadEviction
	c.LoadUpperBoundRatio = o.LoadUpperBoundRatio
	c.LoadLowerBoundRatio = o.LoadLowerBoundRatio
	c.LoadThresholdMetPercentage = o.LoadThresholdMetPercentage
	c.LoadMetricRingSize = o.LoadMetricRingSize
	c.LoadEvictionCoolDownTime = o.LoadEvictionCoolDownTime
	c.EnableSuppressionEviction = o.EnableSuppressionEviction
	c.MaxSuppressionToleranceRate = o.MaxSuppressionToleranceRate
	c.MinSuppressionToleranceDuration = o.MinSuppressionToleranceDuration
	c.GracePeriod = o.GracePeriod

	if err := o.NumaCPUPressureEvictionOptions.ApplyTo(&c.NumaCPUPressureEvictionConfiguration); err != nil {
		return err
	}
	if err := o.NumaSysCPUPressureEvictionOptions.ApplyTo(&c.NumaSysCPUPressureEvictionConfiguration); err != nil {
		return err
	}
	return nil
}
