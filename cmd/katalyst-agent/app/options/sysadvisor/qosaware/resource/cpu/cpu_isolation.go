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

	// IsolatedMaxPoolRatios defines the max ratio for each pool
	// key indicates the pool-name that supports cpu-isolation
	// val indicates the max ratio for this cpu-isolation,
	IsolatedMaxRatios     float32
	IsolatedMaxPoolRatios map[string]string

	// IsolationLockInThreshold and IsolationLockOutPeriodSecs defines the lasting periods
	// before state switches between lock-in and lock-out
	IsolationLockInThreshold   int
	IsolationLockOutPeriodSecs int

	IsolationDisabled      bool
	IsolationDisabledPools []string
}

// NewCPUIsolationOptions creates a new Options with a default config
func NewCPUIsolationOptions() *CPUIsolationOptions {
	return &CPUIsolationOptions{
		IsolationCPURatio: 1.5,
		IsolationCPUSize:  2,

		IsolatedMaxRatios:     0.2,
		IsolatedMaxPoolRatios: map[string]string{},

		IsolationLockInThreshold:   3,
		IsolationLockOutPeriodSecs: 120,

		IsolationDisabled:      true,
		IsolationDisabledPools: []string{},
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUIsolationOptions) AddFlags(fs *pflag.FlagSet) {
	fs.Float32Var(&o.IsolationCPURatio, "isolation-cpu-ratio", o.IsolationCPURatio,
		"mark as isolated if container load exceeds its limit multiplied with this ratio")
	fs.Int32Var(&o.IsolationCPUSize, "isolation-cpu-size", o.IsolationCPUSize,
		"mark as isolated if container load exceeds its limit added with this size")

	fs.Float32Var(&o.IsolatedMaxRatios, "isolation-max-ratios", o.IsolatedMaxRatios,
		"stop marking container isolated if the isolated container exceeds the defined ratio,to avoid "+
			"the left containers lack of enough resources to run; this param works as a default ratio for all pools")
	fs.StringToStringVar(&o.IsolatedMaxPoolRatios, "isolation-max-pool-ratios", o.IsolatedMaxPoolRatios,
		"stop marking container isolated if the isolated container exceeds the defined ratio, to avoid "+
			"the left containers lack of enough resources to run; this param works as separate ratio for all pools")

	fs.IntVar(&o.IsolationLockInThreshold, "isolation-lockin-threshold", o.IsolationLockInThreshold,
		"mark container as isolated iff it reaches the target at least threshold times")
	fs.IntVar(&o.IsolationLockOutPeriodSecs, "isolation-lockout-secs", o.IsolationLockOutPeriodSecs,
		"mark container as back to un-isolated iff it  the target at least threshold times")

	fs.BoolVar(&o.IsolationDisabled, "isolation-disable", o.IsolationDisabled,
		"if set as true, disable the isolation logic")
	fs.StringArrayVar(&o.IsolationDisabledPools, "isolation-disable-pools", o.IsolationDisabledPools,
		"if set as true, disable the isolation logic for the given pool")
}

// ApplyTo fills up config with options
func (o *CPUIsolationOptions) ApplyTo(c *cpu.CPUIsolationConfiguration) error {
	c.IsolationCPURatio = o.IsolationCPURatio
	c.IsolationCPUSize = o.IsolationCPUSize

	c.IsolatedMaxRatios = o.IsolatedMaxRatios
	if o.IsolatedMaxRatios >= 1 {
		return fmt.Errorf("isolation ratio must be smaller than 1")
	}
	for pool, ratioStr := range o.IsolatedMaxPoolRatios {
		ratio, err := strconv.ParseFloat(ratioStr, 32)
		if err != nil {
			return err
		} else if ratio >= 1 {
			return fmt.Errorf("pool %v isolation ratio must be smaller than 1", pool)
		}
		c.IsolatedMaxPoolRatios[pool] = float32(ratio)
	}

	c.IsolationLockInThreshold = o.IsolationLockInThreshold
	c.IsolationLockOutPeriodSecs = o.IsolationLockOutPeriodSecs

	c.IsolationDisabled = o.IsolationDisabled
	c.IsolationDisabledPools = sets.NewString(o.IsolationDisabledPools...)

	return nil
}
