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

package cpu

import (
	"fmt"
	"strconv"

	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/cpu"
)

type CPUIsolationOptions struct {
	// IsolationCPURatio and IsolationCPUSize defines the threshold to trigger isolation
	IsolationCPURatio float32
	IsolationCPUSize  int32

	// IsolatedMaxPoolResourceRatios defines the max ratio for each pool
	// key indicates the pool-name that supports cpu-isolation
	// val indicates the max ratio for this cpu-isolation,
	IsolatedMaxResourceRatio      float32
	IsolatedMaxPoolResourceRatios map[string]string

	// IsolatedMaxPoolPodRatios defines the max pod-amount-ratio for each pool
	// key indicates the pool-name that supports cpu-isolation
	// val indicates the max ratio for this cpu-isolation,
	IsolatedMaxPodRatio      float32
	IsolatedMaxPoolPodRatios map[string]string

	// IsolationLockInThreshold and IsolationLockOutPeriodSecs defines the lasting periods
	// before state switches between lock-in and lock-out
	IsolationLockInThreshold   int
	IsolationLockOutPeriodSecs int

	// IsolationDisabled is used to disable all isolation.
	// IsolationDisabledPools indicates the pools where pods will not be isolated.
	// IsolationForceEnablePools indicates the pools where pods must be isolated, even if the pool
	// is listed in IsolationDisabledPools.
	// IsolationNonExclusivePools indicates the pools where pods will not be exclusively isolated.
	IsolationDisabled          bool
	IsolationDisabledPools     []string
	IsolationForceEnablePools  []string
	IsolationNonExclusivePools []string
}

// NewCPUIsolationOptions creates a new Options with a default config
func NewCPUIsolationOptions() *CPUIsolationOptions {
	return &CPUIsolationOptions{
		IsolationCPURatio: 1.5,
		IsolationCPUSize:  2,

		IsolatedMaxResourceRatio:      0.2,
		IsolatedMaxPoolResourceRatios: map[string]string{},

		IsolatedMaxPodRatio:      0.5,
		IsolatedMaxPoolPodRatios: map[string]string{},

		IsolationLockInThreshold:   3,
		IsolationLockOutPeriodSecs: 120,

		IsolationDisabled:          true,
		IsolationDisabledPools:     []string{},
		IsolationForceEnablePools:  []string{},
		IsolationNonExclusivePools: []string{},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUIsolationOptions) AddFlags(fs *pflag.FlagSet) {
	fs.Float32Var(&o.IsolationCPURatio, "isolation-cpu-ratio", o.IsolationCPURatio,
		"mark as isolated if container load exceeds its limit multiplied with this ratio")
	fs.Int32Var(&o.IsolationCPUSize, "isolation-cpu-size", o.IsolationCPUSize,
		"mark as isolated if container load exceeds its limit added with this size")

	fs.Float32Var(&o.IsolatedMaxResourceRatio, "isolation-max-ratios", o.IsolatedMaxResourceRatio,
		"stop marking container isolated if the isolated container exceeds the defined resource-ratio,to avoid "+
			"the left containers lack of enough resources to run; this param works as a default ratio for all pools")
	fs.StringToStringVar(&o.IsolatedMaxPoolResourceRatios, "isolation-max-pool-ratios", o.IsolatedMaxPoolResourceRatios,
		"stop marking container isolated if the isolated container exceeds the defined resource-ratio, to avoid "+
			"the left containers lack of enough resources to run; this param works as separate ratio for all pools")

	fs.Float32Var(&o.IsolatedMaxPodRatio, "isolation-max-pod-ratios", o.IsolatedMaxPodRatio,
		"stop marking container isolated if the isolated container exceeds the defined pod-amount-ratio,to avoid "+
			"the left containers lack of enough resources to run; this param works as a default ratio for all pools")
	fs.StringToStringVar(&o.IsolatedMaxPoolPodRatios, "isolation-max-pool-pod-ratios", o.IsolatedMaxPoolPodRatios,
		"stop marking container isolated if the isolated container exceeds the defined pod-amount-ratio, to avoid "+
			"the left containers lack of enough resources to run; this param works as separate ratio for all pools")

	fs.IntVar(&o.IsolationLockInThreshold, "isolation-lockin-threshold", o.IsolationLockInThreshold,
		"mark container as isolated iff it reaches the target at least threshold times")
	fs.IntVar(&o.IsolationLockOutPeriodSecs, "isolation-lockout-secs", o.IsolationLockOutPeriodSecs,
		"mark container as back to un-isolated iff it  the target at least threshold times")

	fs.BoolVar(&o.IsolationDisabled, "isolation-disable", o.IsolationDisabled,
		"if set as true, disable the isolation logic")
	fs.StringArrayVar(&o.IsolationDisabledPools, "isolation-disable-pools", o.IsolationDisabledPools,
		"disable the isolation logic for the given pool")
	fs.StringArrayVar(&o.IsolationForceEnablePools, "isolation-force-enable-pools", o.IsolationForceEnablePools,
		"isolation force enable for get given pool")
	fs.StringArrayVar(&o.IsolationNonExclusivePools, "isolation-non-exclusive-pools", o.IsolationNonExclusivePools,
		"isolation is non-exclusive for get given pool")
}

// ApplyTo fills up config with options
func (o *CPUIsolationOptions) ApplyTo(c *cpu.CPUIsolationConfiguration) error {
	c.IsolationCPURatio = o.IsolationCPURatio
	c.IsolationCPUSize = o.IsolationCPUSize

	c.IsolatedMaxResourceRatio = o.IsolatedMaxResourceRatio
	if o.IsolatedMaxResourceRatio >= 1 {
		return fmt.Errorf("isolation resource-ratio must be smaller than 1")
	}
	for pool, ratioStr := range o.IsolatedMaxPoolResourceRatios {
		ratio, err := strconv.ParseFloat(ratioStr, 32)
		if err != nil {
			return err
		} else if ratio >= 1 {
			return fmt.Errorf("pool %v isolation resource-ratio must be smaller than 1", pool)
		}
		c.IsolatedMaxPoolResourceRatios[pool] = float32(ratio)
	}

	c.IsolatedMaxPodRatio = o.IsolatedMaxPodRatio
	if o.IsolatedMaxPodRatio >= 1 {
		return fmt.Errorf("isolation pod-ratio must be smaller than 1")
	}
	for pool, ratioStr := range o.IsolatedMaxPoolPodRatios {
		ratio, err := strconv.ParseFloat(ratioStr, 32)
		if err != nil {
			return err
		} else if ratio >= 1 {
			return fmt.Errorf("pool %v isolation pod-ratio must be smaller than 1", pool)
		}
		c.IsolatedMaxPoolPodRatios[pool] = float32(ratio)
	}

	c.IsolationLockInThreshold = o.IsolationLockInThreshold
	c.IsolationLockOutPeriodSecs = o.IsolationLockOutPeriodSecs

	c.IsolationDisabled = o.IsolationDisabled
	c.IsolationDisabledPools = sets.NewString(o.IsolationDisabledPools...)
	c.IsolationForceEnablePools = sets.NewString(o.IsolationForceEnablePools...)
	c.IsolationNonExclusivePools = sets.NewString(o.IsolationNonExclusivePools...)

	return nil
}
