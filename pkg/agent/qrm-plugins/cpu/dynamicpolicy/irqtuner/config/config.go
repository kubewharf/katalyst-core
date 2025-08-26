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

package config

import (
	"fmt"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	dynconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

type IrqTuningPolicy string

const (
	IrqTuningBalanceFair       IrqTuningPolicy = "balance-fair"
	IrqTuningIrqCoresExclusive IrqTuningPolicy = "irq-cores-exclusive"
	IrqTuningAuto              IrqTuningPolicy = "auto"
)

const (
	IrqTuningIntervalMin       = 3
	EnableRPSCPUVSNicsQueueMin = 1
	ProcessNiceMin             = -20
	ProcessNiceMax             = 19
)

type NicAffinitySocketsPolicy string

const (
	// EachNicBalanceAllSockets each nic's irqs affinity all sockets balancely, no matter how many nics
	EachNicBalanceAllSockets NicAffinitySocketsPolicy = "each-nic-sockets-balance"
	// OverallNicsBalanceAllSockets according number of nics and nics's physical topo binded numa, decide nic's irqs affinity which socket(s)
	OverallNicsBalanceAllSockets NicAffinitySocketsPolicy = "overall-nics-sockets-balance"
	// NicPhysicalTopoBindNuma nic's irqs affinity socket strictly follow whose physical topology binded socket
	NicPhysicalTopoBindNuma NicAffinitySocketsPolicy = "physical-topo-bind"
)

// LowThroughputThresholds thresholds of classifying a nic to low throughput class, if a nic's throughput meet LowThroughputThresholds, then
// this nic will be considered as low througput nic, low throughput nic's irq affinity will be dealed separately, doesnot
// affect normal throughput nic's irq affinity and socket assignments.
// low throughput nic's irq affinity still need to be balanced, but only consider its own socket assignment, and its
// socket assignment only consider its physical topo binded numa.
type LowThroughputThresholds struct {
	RxPPSThreshold  uint64
	SuccessiveCount int
}

type NormalThroughputThresholds struct {
	RxPPSThreshold  uint64
	SuccessiveCount int
}

type ThroughputClassSwitchConfig struct {
	LowThroughputThresholds    LowThroughputThresholds
	NormalThroughputThresholds NormalThroughputThresholds
}

// IrqCoreNetOverloadThresholds when there are one or more irq cores's ratio of softnet_stat 3rd col time_squeeze packets / 1st col processed packets
// greater-equal IrqCoreSoftNetTimeSqueezeRatio,
// then tring to tune irq load balance first, if failed to tune irq load balance, then increase irq cores.
type IrqCoreNetOverloadThresholds struct {
	IrqCoreSoftNetTimeSqueezeRatio float64 // ratio of softnet_stat 3rd col time_squeeze packets / softnet_stat 1st col processed packets
}

// IrqLoadBalanceTuningThresholds when there are one or more irq cores' cpu util greater-equal IrqCoreCpuUtilThreshold or irq cores' net load greater-equal IrqCoreNetOverloadThresholds,
// then tring to tuning irq load balance, that need to find at least one other irq core with relatively low cpu util, their cpu util gap MUST greater-equal IrqCoreCpuUtilGapThresh,
// if succeed to find irq cores with eligible cpu util, then start to tuning load balance,
// or increase irq cores immediately.
type IrqLoadBalanceTuningThresholds struct {
	IrqCoreCpuUtilThreshold    int // irq core cpu util threshold, which will trigger irq cores load balance, generally this value should greater-equal IrqCoresExpectedCpuUtil
	IrqCoreCpuUtilGapThreshold int // threshold of cpu util gap between source core and dest core of irq affinity changing
}

type IrqLoadBalanceConfig struct {
	SuccessiveTuningInterval    int // interval of two successive irq load balance MUST greater-equal this interval
	Thresholds                  IrqLoadBalanceTuningThresholds
	PingPongIntervalThreshold   int // two successive tunes whose interval is less-equal this threshold will be considered as ping-pong tunings
	PingPongCountThresh         int // ping pong count greater-equal this threshold will trigger increasing irq cores
	IrqsTunedNumMaxEachTime     int // max number of irqs are permitted to be tuned from some irq cores to other cores in each time, allowed value {1, 2}
	IrqCoresTunedNumMaxEachTime int // max number of irq cores whose affinity irqs are permitted to tuned to other cores in each time, allowed value {1,2}
}

// IrqCoresIncThresholds when irq cores average cpu util greater-equal IrqCoresAvgCpuUtilThresh, then increase irq cores,
// when there are one or more irq cores's net load greater-equal IrqCoreNetOverloadThresholds, and failed to tune to irq load balance,
// then increase irq cores.
type IrqCoresIncThresholds struct {
	IrqCoresAvgCpuUtilThreshold int // threshold of increasing irq cores, generally this threshold equal to or a litter greater-than IrqCoresExpectedCpuUtil
}

// IrqCoresIncConfig when irq cores cpu util nearly full(e.g., greater-equal 85%), in order to reduce the impact time on the applications, it is necessary to immediately
// fall back to the balance-fair policy first, and later irq tuning manager will auto switch back to IrqCoresExclusive policy based on policies and conditions.
type IrqCoresIncConfig struct {
	SuccessiveIncInterval    int // interval of two successive irq cores increase MUST greater-equal this interval
	IrqCoresCpuFullThreshold int // when irq cores cpu util hit this threshold, then fallback to balance-fair policy
	Thresholds               IrqCoresIncThresholds
}

// IrqCoresDecThresholds when irq cores average cpu util less-equal IrqCoresDecThresholds, then decrease irq cores.
type IrqCoresDecThresholds struct {
	IrqCoresAvgCpuUtilThreshold int // threshold of decreasing irq cores, generally this threshold should be less-than IrqCoresExpectedCpuUtil
}

type IrqCoresDecConfig struct {
	SuccessiveDecInterval    int // interval of two successive irq cores decrease MUST greater-equal this interval
	PingPongAdjustInterval   int // interval of pingpong adjust MUST greater-equal this interval, pingpong adjust means last adjust is increase and current adjust is decrease
	SinceLastBalanceInterval int // interval of decrease and last irq load balance MUST greater-equal this interval
	Thresholds               IrqCoresDecThresholds
	DecCoresMaxEachTime      int // max cores to decrease each time, deault 1
}

type IrqCoresAdjustConfig struct {
	IrqCoresPercentMin int // minimum percent of (100 * irq cores/total(or socket) cores), valid value [0,100], default 2
	IrqCoresPercentMax int // maximum percent of (100 * irq cores/total(or socket) cores), valid value [0,100], default 30
	IrqCoresIncConf    IrqCoresIncConfig
	IrqCoresDecConf    IrqCoresDecConfig
}

// EnableIrqCoresExclusionThresholds when successive count of nic's total PPS >= RxPPSThresh is greater-equal SuccessiveCount,, then enable exclusion of this nic's irq cores.
type EnableIrqCoresExclusionThresholds struct {
	RxPPSThreshold  uint64
	SuccessiveCount int
}

// DisableIrqCoresExclusionThresholds when successive count of nic's total PPS <= RxPPSThresh is greater-equal SuccessiveCount, then disable exclusion of this nic's irq cores.
type DisableIrqCoresExclusionThresholds struct {
	RxPPSThreshold  uint64
	SuccessiveCount int
}

type IrqCoresExclusionThresholds struct {
	EnableThresholds  EnableIrqCoresExclusionThresholds
	DisableThresholds DisableIrqCoresExclusionThresholds
}

type IrqCoresExclusionConfig struct {
	Thresholds               IrqCoresExclusionThresholds
	SuccessiveSwitchInterval float64 // interval of successive enable/disable irq cores exclusion MUST >= SuccessiveSwitchInterval
}

// IrqTuningConfig is the configuration for irq-tuning
type IrqTuningConfig struct {
	Interval                    int
	EnableIrqTuning             bool
	IrqTuningPolicy             IrqTuningPolicy
	EnableRPS                   bool                     // enable rps according to machine specifications configured by kcc, only balance-fair policy support enable rps
	EnableRPSCPUVSNicsQueue     float64                  // enable rps when (cpus count)/(nics queue count) greater than this config
	NicAffinitySocketsPolicy    NicAffinitySocketsPolicy // nics's irqs affinity sockets policy
	IrqCoresExpectedCpuUtil     int
	ThroughputClassSwitchConf   ThroughputClassSwitchConfig
	ReniceIrqCoresKsoftirqd     bool
	IrqCoresKsoftirqdNice       int
	IrqCoreNetOverLoadThreshold IrqCoreNetOverloadThresholds
	IrqLoadBalanceConf          IrqLoadBalanceConfig
	IrqCoresAdjustConf          IrqCoresAdjustConfig
	IrqCoresExclusionConf       IrqCoresExclusionConfig
}

func NewConfiguration() *IrqTuningConfig {
	return &IrqTuningConfig{
		Interval:                 5,
		EnableIrqTuning:          false,
		IrqTuningPolicy:          IrqTuningBalanceFair,
		EnableRPS:                false,
		EnableRPSCPUVSNicsQueue:  0,
		NicAffinitySocketsPolicy: EachNicBalanceAllSockets,
		IrqCoresExpectedCpuUtil:  50,
		ThroughputClassSwitchConf: ThroughputClassSwitchConfig{
			LowThroughputThresholds: LowThroughputThresholds{
				RxPPSThreshold:  3000,
				SuccessiveCount: 30,
			},
			NormalThroughputThresholds: NormalThroughputThresholds{
				RxPPSThreshold:  6000,
				SuccessiveCount: 10,
			},
		},
		ReniceIrqCoresKsoftirqd: false,
		IrqCoresKsoftirqdNice:   -20,
		IrqCoreNetOverLoadThreshold: IrqCoreNetOverloadThresholds{
			IrqCoreSoftNetTimeSqueezeRatio: 0.1,
		},
		IrqLoadBalanceConf: IrqLoadBalanceConfig{
			SuccessiveTuningInterval: 10,
			Thresholds: IrqLoadBalanceTuningThresholds{
				IrqCoreCpuUtilThreshold:    65,
				IrqCoreCpuUtilGapThreshold: 20,
			},
			PingPongIntervalThreshold:   180,
			PingPongCountThresh:         1,
			IrqsTunedNumMaxEachTime:     2,
			IrqCoresTunedNumMaxEachTime: 1,
		},
		IrqCoresAdjustConf: IrqCoresAdjustConfig{
			IrqCoresPercentMin: 2,
			IrqCoresPercentMax: 30,
			IrqCoresIncConf: IrqCoresIncConfig{
				SuccessiveIncInterval:    5,
				IrqCoresCpuFullThreshold: 85,
				Thresholds: IrqCoresIncThresholds{
					IrqCoresAvgCpuUtilThreshold: 60,
				},
			},
			IrqCoresDecConf: IrqCoresDecConfig{
				SuccessiveDecInterval:    30,
				PingPongAdjustInterval:   300,
				SinceLastBalanceInterval: 60,
				Thresholds: IrqCoresDecThresholds{
					IrqCoresAvgCpuUtilThreshold: 40,
				},
				DecCoresMaxEachTime: 1,
			},
		},
		IrqCoresExclusionConf: IrqCoresExclusionConfig{
			Thresholds: IrqCoresExclusionThresholds{
				EnableThresholds: EnableIrqCoresExclusionThresholds{
					RxPPSThreshold:  60000,
					SuccessiveCount: 30,
				},
				DisableThresholds: DisableIrqCoresExclusionThresholds{
					RxPPSThreshold:  30000,
					SuccessiveCount: 30,
				},
			},
			SuccessiveSwitchInterval: 600,
		},
	}
}

func (c *IrqTuningConfig) String() string {
	msg := "IrqTuningConfig:\n"

	msg = fmt.Sprintf("%s    Interval: %d\n", msg, c.Interval)
	msg = fmt.Sprintf("%s    EnableIrqTuning: %t\n", msg, c.EnableIrqTuning)
	msg = fmt.Sprintf("%s    IrqTuningPolicy: %s\n", msg, c.IrqTuningPolicy)
	msg = fmt.Sprintf("%s    EnableRPS: %t\n", msg, c.EnableRPS)
	msg = fmt.Sprintf("%s    EnableRPSCPUVSNicsQueue: %f\n", msg, c.EnableRPSCPUVSNicsQueue)
	msg = fmt.Sprintf("%s    NicAffinitySocketsPolicy: %s\n", msg, c.NicAffinitySocketsPolicy)
	msg = fmt.Sprintf("%s    IrqCoresExpectedCpuUtil: %d\n", msg, c.IrqCoresExpectedCpuUtil)
	msg = fmt.Sprintf("%s    ThroughputClassSwitchConf:\n", msg)
	msg = fmt.Sprintf("%s        LowThroughputThresholds:\n", msg)
	msg = fmt.Sprintf("%s            RxPPSThreshold: %d\n", msg, c.ThroughputClassSwitchConf.LowThroughputThresholds.RxPPSThreshold)
	msg = fmt.Sprintf("%s            SuccessiveCount: %d\n", msg, c.ThroughputClassSwitchConf.LowThroughputThresholds.SuccessiveCount)
	msg = fmt.Sprintf("%s        NormalThroughputThresholds:\n", msg)
	msg = fmt.Sprintf("%s            RxPPSThreshold: %d\n", msg, c.ThroughputClassSwitchConf.NormalThroughputThresholds.RxPPSThreshold)
	msg = fmt.Sprintf("%s            SuccessiveCount: %d\n", msg, c.ThroughputClassSwitchConf.NormalThroughputThresholds.SuccessiveCount)
	msg = fmt.Sprintf("%s    ReniceIrqCoresKsoftirqd: %t\n", msg, c.ReniceIrqCoresKsoftirqd)
	msg = fmt.Sprintf("%s    IrqCoresKsoftirqdNice: %d\n", msg, c.IrqCoresKsoftirqdNice)
	msg = fmt.Sprintf("%s    IrqCoreNetOverLoadThreshold:\n", msg)
	msg = fmt.Sprintf("%s        IrqCoreSoftNetTimeSqueezeRatio: %f\n", msg, c.IrqCoreNetOverLoadThreshold.IrqCoreSoftNetTimeSqueezeRatio)
	msg = fmt.Sprintf("%s    IrqLoadBalanceConf:\n", msg)
	msg = fmt.Sprintf("%s        SuccessiveTuningInterval: %d\n", msg, c.IrqLoadBalanceConf.SuccessiveTuningInterval)
	msg = fmt.Sprintf("%s        Thresholds:\n", msg)
	msg = fmt.Sprintf("%s            IrqCoreCpuUtilThresh: %d\n", msg, c.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilThreshold)
	msg = fmt.Sprintf("%s            IrqCoreCpuUtilGapThresh: %d\n", msg, c.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilGapThreshold)
	msg = fmt.Sprintf("%s        PingPongIntervalThresh: %d\n", msg, c.IrqLoadBalanceConf.PingPongIntervalThreshold)
	msg = fmt.Sprintf("%s        PingPongCountThresh: %d\n", msg, c.IrqLoadBalanceConf.PingPongCountThresh)
	msg = fmt.Sprintf("%s        IrqsTunedNumMaxEachTime: %d\n", msg, c.IrqLoadBalanceConf.IrqsTunedNumMaxEachTime)
	msg = fmt.Sprintf("%s        IrqCoresTunedNumMaxEachTime: %d\n", msg, c.IrqLoadBalanceConf.IrqCoresTunedNumMaxEachTime)
	msg = fmt.Sprintf("%s    IrqCoresAdjustConf:\n", msg)
	msg = fmt.Sprintf("%s        IrqCoresPercentMin: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresPercentMin)
	msg = fmt.Sprintf("%s        IrqCoresPercentMax: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresPercentMax)
	msg = fmt.Sprintf("%s        IrqCoresIncConf:\n", msg)
	msg = fmt.Sprintf("%s            SuccessiveIncInterval: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresIncConf.SuccessiveIncInterval)
	msg = fmt.Sprintf("%s            IrqCoresCpuFullThresh: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresIncConf.IrqCoresCpuFullThreshold)
	msg = fmt.Sprintf("%s            Thresholds\n", msg)
	msg = fmt.Sprintf("%s                IrqCoresAvgCpuUtilThresh: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresIncConf.Thresholds.IrqCoresAvgCpuUtilThreshold)
	msg = fmt.Sprintf("%s        IrqCoresDecConf:\n", msg)
	msg = fmt.Sprintf("%s            SuccessiveDecInterval: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.SuccessiveDecInterval)
	msg = fmt.Sprintf("%s            PingPongAdjustInterval: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.PingPongAdjustInterval)
	msg = fmt.Sprintf("%s            SinceLastBalanceInterval: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.SinceLastBalanceInterval)
	msg = fmt.Sprintf("%s            Thresholds\n", msg)
	msg = fmt.Sprintf("%s                IrqCoresAvgCpuUtilThresh: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.Thresholds.IrqCoresAvgCpuUtilThreshold)
	msg = fmt.Sprintf("%s            DecCoresMaxEachTime: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.DecCoresMaxEachTime)
	msg = fmt.Sprintf("%s    IrqCoresExclusionConf:\n", msg)
	msg = fmt.Sprintf("%s        Thresholds:\n", msg)
	msg = fmt.Sprintf("%s            EnableThresholds:\n", msg)
	msg = fmt.Sprintf("%s                RxPPSThreshold: %d\n", msg, c.IrqCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThreshold)
	msg = fmt.Sprintf("%s                SuccessiveCount: %d\n", msg, c.IrqCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount)
	msg = fmt.Sprintf("%s            DisableThresholds:\n", msg)
	msg = fmt.Sprintf("%s                RxPPSThreshold: %d\n", msg, c.IrqCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThreshold)
	msg = fmt.Sprintf("%s                SuccessiveCount: %d\n", msg, c.IrqCoresExclusionConf.Thresholds.DisableThresholds.SuccessiveCount)
	msg = fmt.Sprintf("%s        SuccessiveSwitchInterval: %f", msg, c.IrqCoresExclusionConf.SuccessiveSwitchInterval)

	return msg
}

func (c *IrqTuningConfig) Equal(other *IrqTuningConfig) bool {
	if other == nil {
		return false
	}

	if c.Interval != other.Interval ||
		c.EnableIrqTuning != other.EnableIrqTuning ||
		c.IrqTuningPolicy != other.IrqTuningPolicy ||
		c.EnableRPS != other.EnableRPS ||
		c.EnableRPSCPUVSNicsQueue != other.EnableRPSCPUVSNicsQueue ||
		c.NicAffinitySocketsPolicy != other.NicAffinitySocketsPolicy ||
		c.IrqCoresExpectedCpuUtil != other.IrqCoresExpectedCpuUtil {
		return false
	}

	if c.ThroughputClassSwitchConf.LowThroughputThresholds.RxPPSThreshold != other.ThroughputClassSwitchConf.LowThroughputThresholds.RxPPSThreshold ||
		c.ThroughputClassSwitchConf.LowThroughputThresholds.SuccessiveCount != other.ThroughputClassSwitchConf.LowThroughputThresholds.SuccessiveCount ||
		c.ThroughputClassSwitchConf.NormalThroughputThresholds.RxPPSThreshold != other.ThroughputClassSwitchConf.NormalThroughputThresholds.RxPPSThreshold ||
		c.ThroughputClassSwitchConf.NormalThroughputThresholds.SuccessiveCount != other.ThroughputClassSwitchConf.NormalThroughputThresholds.SuccessiveCount {
		return false
	}

	if c.ReniceIrqCoresKsoftirqd != other.ReniceIrqCoresKsoftirqd ||
		c.IrqCoresKsoftirqdNice != other.IrqCoresKsoftirqdNice {
		return false
	}

	if c.IrqCoreNetOverLoadThreshold.IrqCoreSoftNetTimeSqueezeRatio != other.IrqCoreNetOverLoadThreshold.IrqCoreSoftNetTimeSqueezeRatio {
		return false
	}

	if c.IrqLoadBalanceConf.SuccessiveTuningInterval != other.IrqLoadBalanceConf.SuccessiveTuningInterval ||
		c.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilThreshold != other.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilThreshold ||
		c.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilGapThreshold != other.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilGapThreshold ||
		c.IrqLoadBalanceConf.PingPongIntervalThreshold != other.IrqLoadBalanceConf.PingPongIntervalThreshold ||
		c.IrqLoadBalanceConf.PingPongCountThresh != other.IrqLoadBalanceConf.PingPongCountThresh ||
		c.IrqLoadBalanceConf.IrqsTunedNumMaxEachTime != other.IrqLoadBalanceConf.IrqsTunedNumMaxEachTime ||
		c.IrqLoadBalanceConf.IrqCoresTunedNumMaxEachTime != other.IrqLoadBalanceConf.IrqCoresTunedNumMaxEachTime {
		return false
	}

	if c.IrqCoresAdjustConf.IrqCoresPercentMin != other.IrqCoresAdjustConf.IrqCoresPercentMin ||
		c.IrqCoresAdjustConf.IrqCoresPercentMax != other.IrqCoresAdjustConf.IrqCoresPercentMax ||
		c.IrqCoresAdjustConf.IrqCoresIncConf.SuccessiveIncInterval != other.IrqCoresAdjustConf.IrqCoresIncConf.SuccessiveIncInterval ||
		c.IrqCoresAdjustConf.IrqCoresIncConf.IrqCoresCpuFullThreshold != other.IrqCoresAdjustConf.IrqCoresIncConf.IrqCoresCpuFullThreshold ||
		c.IrqCoresAdjustConf.IrqCoresIncConf.Thresholds.IrqCoresAvgCpuUtilThreshold != other.IrqCoresAdjustConf.IrqCoresIncConf.Thresholds.IrqCoresAvgCpuUtilThreshold ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.SuccessiveDecInterval != other.IrqCoresAdjustConf.IrqCoresDecConf.SuccessiveDecInterval ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.PingPongAdjustInterval != other.IrqCoresAdjustConf.IrqCoresDecConf.PingPongAdjustInterval ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.SinceLastBalanceInterval != other.IrqCoresAdjustConf.IrqCoresDecConf.SinceLastBalanceInterval ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.Thresholds.IrqCoresAvgCpuUtilThreshold != other.IrqCoresAdjustConf.IrqCoresDecConf.Thresholds.IrqCoresAvgCpuUtilThreshold ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.DecCoresMaxEachTime != other.IrqCoresAdjustConf.IrqCoresDecConf.DecCoresMaxEachTime {
		return false
	}

	if c.IrqCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThreshold != other.IrqCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThreshold ||
		c.IrqCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount != other.IrqCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount ||
		c.IrqCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThreshold != other.IrqCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThreshold ||
		c.IrqCoresExclusionConf.Thresholds.DisableThresholds.SuccessiveCount != other.IrqCoresExclusionConf.Thresholds.DisableThresholds.SuccessiveCount ||
		c.IrqCoresExclusionConf.SuccessiveSwitchInterval != other.IrqCoresExclusionConf.SuccessiveSwitchInterval {
		return false
	}

	return true
}

func ValidateIrqTuningDynamicConfig(dynamicConf *dynconfig.Configuration) error {
	if dynamicConf == nil {
		return fmt.Errorf("dynamic config is empty")
	}

	conf := dynamicConf.IRQTuningConfiguration
	if conf == nil {
		return nil
	}

	if conf.TuningInterval < IrqTuningIntervalMin {
		return fmt.Errorf("invalid TuningInterval: %d, less-than IrqTuingMinInterval: %d", conf.TuningInterval, IrqTuningIntervalMin)
	}

	if conf.EnableRPSCPUVSNicsQueue < EnableRPSCPUVSNicsQueueMin && conf.EnableRPSCPUVSNicsQueue != 0 {
		return fmt.Errorf("invalid EnableRPSCPUVSNicsQueue: %f, less-than EnableRPSCPUVSNicsQueueMin %d", conf.EnableRPSCPUVSNicsQueue, EnableRPSCPUVSNicsQueueMin)
	}

	if conf.KsoftirqdNice < ProcessNiceMin || conf.KsoftirqdNice > ProcessNiceMax {
		return fmt.Errorf("invalid KsoftirqdNice: %d", conf.KsoftirqdNice)
	}

	if conf.CoresExpectedCPUUtil <= 0 || conf.CoresExpectedCPUUtil >= 100 {
		return fmt.Errorf("invalid CoresExpectedCPUUtil: %d", conf.CoresExpectedCPUUtil)
	}

	if conf.ThroughputClassSwitchConf != nil {
		if conf.ThroughputClassSwitchConf.LowThresholdConfig != nil {
			if conf.ThroughputClassSwitchConf.LowThresholdConfig.SuccessiveCount <= 0 {
				return fmt.Errorf("invalid ThroughputClassSwitchConf.LowThresholdConfig.SuccessiveCount: %d", conf.ThroughputClassSwitchConf.LowThresholdConfig.SuccessiveCount)
			}
		}

		if conf.ThroughputClassSwitchConf.NormalThresholdConfig != nil {
			if conf.ThroughputClassSwitchConf.NormalThresholdConfig.SuccessiveCount <= 0 {
				return fmt.Errorf("invalid NormalThresholdConfig.LowThresholdConfig.SuccessiveCount: %d", conf.ThroughputClassSwitchConf.NormalThresholdConfig.SuccessiveCount)
			}
		}

		if conf.ThroughputClassSwitchConf.LowThresholdConfig != nil &&
			conf.ThroughputClassSwitchConf.NormalThresholdConfig != nil {
			if conf.ThroughputClassSwitchConf.LowThresholdConfig.RxPPSThreshold >= conf.ThroughputClassSwitchConf.NormalThresholdConfig.RxPPSThreshold {
				return fmt.Errorf("ThroughputClassSwitchConf.LowThresholdConfig.RxPPSThreshold: %d greater-equal ThroughputClassSwitchConf.NormalThresholdConfig.RxPPSThreshold: %d",
					conf.ThroughputClassSwitchConf.LowThresholdConfig.RxPPSThreshold, conf.ThroughputClassSwitchConf.NormalThresholdConfig.RxPPSThreshold)
			}
		}
	}

	if conf.LoadBalanceConf != nil {
		lbConf := conf.LoadBalanceConf

		if lbConf.SuccessiveTuningInterval < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.SuccessiveTuningInterval: %d", lbConf.SuccessiveTuningInterval)
		}

		if lbConf.PingPongIntervalThreshold < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.PingPongIntervalThresh: %d", lbConf.PingPongIntervalThreshold)
		}

		if lbConf.PingPongCountThreshold < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.PingPongCountThresh: %d", lbConf.PingPongCountThreshold)
		}

		if lbConf.IRQsTunedNumMaxEachTime < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.IRQsTunedNumMaxEachTime: %d", lbConf.IRQsTunedNumMaxEachTime)
		}

		if lbConf.IRQCoresTunedNumMaxEachTime < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.IRQsTunedNumMaxEachTime: %d", lbConf.IRQCoresTunedNumMaxEachTime)
		}

		if lbConf.Thresholds != nil {
			if lbConf.Thresholds.CPUUtilThreshold <= 0 || lbConf.Thresholds.CPUUtilThreshold >= 100 {
				return fmt.Errorf("invalid LoadBalanceConf.Thresholds.CPUUtilThresh: %d", lbConf.Thresholds.CPUUtilThreshold)
			}

			if lbConf.Thresholds.CPUUtilThreshold <= conf.CoresExpectedCPUUtil {
				return fmt.Errorf("LoadBalanceConf.Thresholds.CPUUtilThresh %d less-equal CoresExpectedCPUUtil: %d",
					lbConf.Thresholds.CPUUtilThreshold, conf.CoresExpectedCPUUtil)
			}

			if conf.CoresAdjustConf != nil && conf.CoresAdjustConf.IncConf != nil && conf.CoresAdjustConf.IncConf.Thresholds != nil {
				// LoadBalanceConf.Thresholds.CPUUtilThresh (threshold of balancing irqs) should greater than
				// CoresAdjustConf.IncConf.Thresholds.AvgCPUUtil (threshold of increasing irq cores),
				// it's meaningless to let LoadBalanceConf.Thresholds.CPUUtilThresh less-equal CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh.
				if lbConf.Thresholds.CPUUtilThreshold <= conf.CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThreshold {
					return fmt.Errorf("LoadBalanceConf.Thresholds.CPUUtilThresh %d less-equal CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh %d",
						lbConf.Thresholds.CPUUtilThreshold, conf.CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThreshold)
				}
			}

			if lbConf.Thresholds.CPUUtilGapThreshold <= 0 || lbConf.Thresholds.CPUUtilGapThreshold >= 100 {
				return fmt.Errorf("invalid LoadBalanceConf.Thresholds.CPUUtilGapThresh: %d", lbConf.Thresholds.CPUUtilGapThreshold)
			}
		}
	}

	if conf.CoresAdjustConf != nil {
		adjConf := conf.CoresAdjustConf

		if adjConf.PercentMin <= 0 || adjConf.PercentMin >= 100 {
			return fmt.Errorf("invalid CoresAdjustConf.PercentMin: %d", adjConf.PercentMin)
		}

		if adjConf.PercentMax <= 0 || adjConf.PercentMax >= 100 {
			return fmt.Errorf("invalid CoresAdjustConf.PercentMax: %d", adjConf.PercentMax)
		}

		if adjConf.PercentMin >= adjConf.PercentMax {
			return fmt.Errorf("CoresAdjustConf.PercentMin %d greather-equal CoresAdjustConf.PercentMax: %d", adjConf.PercentMin, adjConf.PercentMax)
		}

		if adjConf.IncConf != nil {
			incConf := adjConf.IncConf

			if incConf.SuccessiveIncInterval < 1 {
				return fmt.Errorf("invalid CoresAdjustConf.IncConf.SuccessiveIncInterval: %d", incConf.SuccessiveIncInterval)
			}

			if incConf.FullThreshold <= 0 || incConf.FullThreshold > 100 {
				return fmt.Errorf("CoresAdjustConf.IncConf.FullThresh: %d", incConf.FullThreshold)
			}

			if incConf.Thresholds != nil {
				if incConf.FullThreshold <= incConf.Thresholds.AvgCPUUtilThreshold {
					return fmt.Errorf("CoresAdjustConf.IncConf.FullThresh %d less-equal CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh %d",
						incConf.FullThreshold, incConf.Thresholds.AvgCPUUtilThreshold)
				}

				if incConf.Thresholds.AvgCPUUtilThreshold <= 0 || incConf.Thresholds.AvgCPUUtilThreshold >= 100 {
					return fmt.Errorf("invalid CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh %d", incConf.Thresholds.AvgCPUUtilThreshold)
				}

				if incConf.Thresholds.AvgCPUUtilThreshold <= conf.CoresExpectedCPUUtil {
					return fmt.Errorf("CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh %d less-equal CoresExpectedCPUUtil %d",
						incConf.Thresholds.AvgCPUUtilThreshold, conf.CoresExpectedCPUUtil)
				}
			}
		}

		if adjConf.DecConf != nil {
			decConf := adjConf.DecConf

			if decConf.SuccessiveDecInterval < 1 {
				return fmt.Errorf("invalid CoresAdjustConf.DecConf.SuccessiveDecInterval: %d", decConf.SuccessiveDecInterval)
			}

			if decConf.PingPongAdjustInterval < 1 {
				return fmt.Errorf("invalid CoresAdjustConf.DecConf.PingPongAdjustInterval: %d", decConf.PingPongAdjustInterval)
			}

			if decConf.SinceLastBalanceInterval < 1 {
				return fmt.Errorf("invalid CoresAdjustConf.DecConf.SinceLastBalanceInterval: %d", decConf.SinceLastBalanceInterval)
			}

			if decConf.DecCoresMaxEachTime < 1 {
				return fmt.Errorf("invalid CoresAdjustConf.DecConf.DecCoresMaxEachTime: %d", decConf.DecCoresMaxEachTime)
			}

			if decConf.Thresholds != nil {
				if decConf.Thresholds.AvgCPUUtilThreshold <= 0 || decConf.Thresholds.AvgCPUUtilThreshold >= 100 {
					return fmt.Errorf("invalid CoresAdjustConf.DecConf.Thresholds.AvgCPUUtilThresh: %d", decConf.Thresholds.AvgCPUUtilThreshold)
				}

				if decConf.Thresholds.AvgCPUUtilThreshold >= conf.CoresExpectedCPUUtil {
					return fmt.Errorf("CoresAdjustConf.DecConf.Thresholds.AvgCPUUtilThresh %d greater-equal CoresExpectedCPUUtil %d",
						decConf.Thresholds.AvgCPUUtilThreshold, conf.CoresExpectedCPUUtil)
				}
			}
		}
	}

	if conf.CoresExclusionConf != nil {
		exclConf := conf.CoresExclusionConf

		if exclConf.SuccessiveSwitchInterval < 1 {
			return fmt.Errorf("invalid CoresExclusionConf.SuccessiveSwitchInterval: %f", exclConf.SuccessiveSwitchInterval)
		}

		if exclConf.Thresholds != nil {
			if exclConf.Thresholds.EnableThresholds != nil {
				if exclConf.Thresholds.EnableThresholds.SuccessiveCount <= 0 {
					return fmt.Errorf("invalid CoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount %d", exclConf.Thresholds.EnableThresholds.SuccessiveCount)
				}
			}

			if exclConf.Thresholds.DisableThresholds != nil {
				if exclConf.Thresholds.DisableThresholds.SuccessiveCount <= 0 {
					return fmt.Errorf("invalid CoresExclusionConf.Thresholds.DisableThresholds.SuccessiveCount %d", exclConf.Thresholds.DisableThresholds.SuccessiveCount)
				}
			}

			if exclConf.Thresholds.EnableThresholds != nil &&
				exclConf.Thresholds.DisableThresholds != nil {
				if exclConf.Thresholds.DisableThresholds.RxPPSThreshold >= exclConf.Thresholds.EnableThresholds.RxPPSThreshold {
					return fmt.Errorf("CoresExclusionConf.Thresholds.DisableThresholds.RxPPSThreshold: %d greater-equal CoresExclusionConf.Thresholds.EnableThresholds.RxPPSThreshold: %d",
						exclConf.Thresholds.DisableThresholds.RxPPSThreshold, exclConf.Thresholds.EnableThresholds.RxPPSThreshold)
				}
			}
		}
	}

	return nil
}

func ConvertDynamicConfigToIrqTuningConfig(dynamicConf *dynconfig.Configuration) *IrqTuningConfig {
	conf := NewConfiguration()

	if dynamicConf == nil {
		return conf
	}

	if dynamicConf.IRQTuningConfiguration != nil {
		conf.Interval = dynamicConf.IRQTuningConfiguration.TuningInterval
		conf.EnableIrqTuning = dynamicConf.IRQTuningConfiguration.EnableTuner

		switch dynamicConf.IRQTuningConfiguration.TuningPolicy {
		case v1alpha1.TuningPolicyExclusive:
			conf.IrqTuningPolicy = IrqTuningIrqCoresExclusive
		case v1alpha1.TuningPolicyAuto:
			conf.IrqTuningPolicy = IrqTuningAuto
		case v1alpha1.TuningPolicyBalance:
			fallthrough
		default:
			conf.IrqTuningPolicy = IrqTuningBalanceFair
		}

		conf.EnableRPS = dynamicConf.IRQTuningConfiguration.EnableRPS
		conf.EnableRPSCPUVSNicsQueue = dynamicConf.IRQTuningConfiguration.EnableRPSCPUVSNicsQueue

		switch dynamicConf.IRQTuningConfiguration.NICAffinityPolicy {
		case v1alpha1.NICAffinityPolicyPhysicalTopo:
			conf.NicAffinitySocketsPolicy = NicPhysicalTopoBindNuma
		case v1alpha1.NICAffinityPolicyCompleteMap:
			conf.NicAffinitySocketsPolicy = EachNicBalanceAllSockets
		case v1alpha1.NICAffinityPolicyOverallBalance:
			fallthrough
		default:
			conf.NicAffinitySocketsPolicy = OverallNicsBalanceAllSockets
		}

		conf.IrqCoresExpectedCpuUtil = dynamicConf.IRQTuningConfiguration.CoresExpectedCPUUtil

		if dynamicConf.IRQTuningConfiguration.ThroughputClassSwitchConf != nil {
			throughputClassSwitchConf := dynamicConf.IRQTuningConfiguration.ThroughputClassSwitchConf
			if throughputClassSwitchConf.LowThresholdConfig != nil {
				conf.ThroughputClassSwitchConf.LowThroughputThresholds.RxPPSThreshold = throughputClassSwitchConf.LowThresholdConfig.RxPPSThreshold
				conf.ThroughputClassSwitchConf.LowThroughputThresholds.SuccessiveCount = throughputClassSwitchConf.LowThresholdConfig.SuccessiveCount
			}

			if throughputClassSwitchConf.NormalThresholdConfig != nil {
				conf.ThroughputClassSwitchConf.NormalThroughputThresholds.RxPPSThreshold = throughputClassSwitchConf.NormalThresholdConfig.RxPPSThreshold
				conf.ThroughputClassSwitchConf.NormalThroughputThresholds.SuccessiveCount = throughputClassSwitchConf.NormalThresholdConfig.SuccessiveCount
			}
		}

		conf.ReniceIrqCoresKsoftirqd = dynamicConf.IRQTuningConfiguration.ReniceKsoftirqd
		conf.IrqCoresKsoftirqdNice = dynamicConf.IRQTuningConfiguration.KsoftirqdNice

		if dynamicConf.IRQTuningConfiguration.CoreNetOverLoadThreshold != nil {
			conf.IrqCoreNetOverLoadThreshold.IrqCoreSoftNetTimeSqueezeRatio = dynamicConf.IRQTuningConfiguration.CoreNetOverLoadThreshold.SoftNetTimeSqueezeRatio
		}

		if dynamicConf.IRQTuningConfiguration.LoadBalanceConf != nil {
			dynLoadBalanceConf := dynamicConf.IRQTuningConfiguration.LoadBalanceConf
			conf.IrqLoadBalanceConf.SuccessiveTuningInterval = dynLoadBalanceConf.SuccessiveTuningInterval
			if dynLoadBalanceConf.Thresholds != nil {
				conf.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilThreshold = dynLoadBalanceConf.Thresholds.CPUUtilThreshold
				conf.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilGapThreshold = dynLoadBalanceConf.Thresholds.CPUUtilGapThreshold
			}
			conf.IrqLoadBalanceConf.PingPongIntervalThreshold = dynLoadBalanceConf.PingPongIntervalThreshold
			conf.IrqLoadBalanceConf.PingPongCountThresh = dynLoadBalanceConf.PingPongCountThreshold
			conf.IrqLoadBalanceConf.IrqsTunedNumMaxEachTime = dynLoadBalanceConf.IRQsTunedNumMaxEachTime
			conf.IrqLoadBalanceConf.IrqCoresTunedNumMaxEachTime = dynLoadBalanceConf.IRQCoresTunedNumMaxEachTime
		}

		if dynamicConf.IRQTuningConfiguration.CoresAdjustConf != nil {
			dynCoresAdjustConf := dynamicConf.IRQTuningConfiguration.CoresAdjustConf
			conf.IrqCoresAdjustConf.IrqCoresPercentMin = dynCoresAdjustConf.PercentMin
			conf.IrqCoresAdjustConf.IrqCoresPercentMax = dynCoresAdjustConf.PercentMax

			if dynCoresAdjustConf.IncConf != nil {
				conf.IrqCoresAdjustConf.IrqCoresIncConf.SuccessiveIncInterval = dynCoresAdjustConf.IncConf.SuccessiveIncInterval
				conf.IrqCoresAdjustConf.IrqCoresIncConf.IrqCoresCpuFullThreshold = dynCoresAdjustConf.IncConf.FullThreshold
				if dynCoresAdjustConf.IncConf.Thresholds != nil {
					conf.IrqCoresAdjustConf.IrqCoresIncConf.Thresholds.IrqCoresAvgCpuUtilThreshold = dynCoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThreshold
				}
			}

			if dynCoresAdjustConf.DecConf != nil {
				conf.IrqCoresAdjustConf.IrqCoresDecConf.SuccessiveDecInterval = dynCoresAdjustConf.DecConf.SuccessiveDecInterval
				conf.IrqCoresAdjustConf.IrqCoresDecConf.PingPongAdjustInterval = dynCoresAdjustConf.DecConf.PingPongAdjustInterval
				conf.IrqCoresAdjustConf.IrqCoresDecConf.SinceLastBalanceInterval = dynCoresAdjustConf.DecConf.SinceLastBalanceInterval
				if dynCoresAdjustConf.DecConf.Thresholds != nil {
					conf.IrqCoresAdjustConf.IrqCoresDecConf.Thresholds.IrqCoresAvgCpuUtilThreshold = dynCoresAdjustConf.DecConf.Thresholds.AvgCPUUtilThreshold
				}
				conf.IrqCoresAdjustConf.IrqCoresDecConf.DecCoresMaxEachTime = dynCoresAdjustConf.DecConf.DecCoresMaxEachTime
			}
		}

		if dynamicConf.IRQTuningConfiguration.CoresExclusionConf != nil {
			dynCoresExclusionConf := dynamicConf.IRQTuningConfiguration.CoresExclusionConf
			if dynCoresExclusionConf.Thresholds != nil {
				if dynCoresExclusionConf.Thresholds.EnableThresholds != nil {
					conf.IrqCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThreshold = dynCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThreshold
					conf.IrqCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount = dynCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount
				}
				if dynCoresExclusionConf.Thresholds.DisableThresholds != nil {
					conf.IrqCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThreshold = dynCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThreshold
					conf.IrqCoresExclusionConf.Thresholds.DisableThresholds.SuccessiveCount = dynCoresExclusionConf.Thresholds.DisableThresholds.SuccessiveCount
				}
				conf.IrqCoresExclusionConf.SuccessiveSwitchInterval = dynCoresExclusionConf.SuccessiveSwitchInterval
			}
		}
	}

	return conf
}
