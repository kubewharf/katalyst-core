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
	IrqTuingIntervalMin        = 3
	EnableRPSCPUVSNicsQueueMin = 1
	ProcessNiceMin             = -20
	ProcessNiceMax             = 19
)

type NicAffinitySocketsPolicy string

const (
	// no matter how many nics, each nic's irqs affinity all sockets balancely
	EachNicBalanceAllSockets NicAffinitySocketsPolicy = "each-nic-sockets-balance"
	// according number of nics and nics's physical topo binded numa, decide nic's irqs affinity which socket(s)
	OverallNicsBalanceAllSockets NicAffinitySocketsPolicy = "overall-nics-sockets-balance"
	// nic's irqs affitnied socket strictly follow whose physical topology binded socket
	NicPhysicalTopoBindNuma NicAffinitySocketsPolicy = "physical-topo-bind"
)

// thresholds of classifying a nic to low throughput class, if a nic's throughput meet LowThroughputThresholds, then
// this nic will be considered as low througput nic, low throughput nic's irq affinity will be dealed separately, doesnot
// affect normal throughput nic's irq affinity and socket assignments.
// low throughput nic's irq affinity still need to be balanced, but only consider its own socket assignment, and its
// socket assignment only consider its physical topo binded numa.
type LowThroughputThresholds struct {
	RxPPSThresh     uint64
	SuccessiveCount int
}

type NormalThroughputThresholds struct {
	RxPPSThresh     uint64
	SuccessiveCount int
}

type ThroughputClassSwitchConfig struct {
	LowThroughputThresholds    LowThroughputThresholds
	NormalThroughputThresholds NormalThroughputThresholds
}

// when there are one or more irq cores's ratio of softnet_stat 3rd col time_squeeze packets / 1st col processed packets
// greater-equal IrqCoreSoftNetTimeSqueezeRatio,
// then tring to tune irq load balance first, if failed to tune irq load balance, then increase irq cores.
type IrqCoreNetOverloadThresholds struct {
	IrqCoreSoftNetTimeSqueezeRatio float64 // ratio of softnet_stat 3rd col time_squeeze packets / softnet_stat 1st col processed packets
}

// when there are one or more irq cores's cpu util greater-equal IrqCoreCpuUtilThresh or irq cores's net load greater-equal IrqCoreNetOverloadThresholds,
// then tring to tuning irq load balance, that need to find at least one other irq core with relatively low cpu util, their cpu util gap MUST greater-equal IrqCoreCpuUtilGapThresh,
// if succeed to find irq cores with eligible cpu util, then start to tuning load balance,
// or increase irq cores immediately.
type IrqLoadBalanceTuningThresholds struct {
	IrqCoreCpuUtilThresh    int // irq core cpu util threshold, which will trigger irq cores load balance, generally this value should greater-equal IrqCoresExpectedCpuUtil
	IrqCoreCpuUtilGapThresh int // threshold of cpu util gap between source core and dest core of irq affinity changing
}

type IrqLoadBalanceConfig struct {
	SuccessiveTuningInterval    int // interval of two successive irq load balance MUST greater-equal this interval
	Thresholds                  IrqLoadBalanceTuningThresholds
	PingPongIntervalThresh      int // two successive tunes whose interval is less-equal this threshold will be considered as pingpong tunings
	PingPongCountThresh         int // ping pong count greater-equal this threshold will trigger increasing irq cores
	IrqsTunedNumMaxEachTime     int // max number of irqs are permitted to be tuned from some irq cores to other cores in each time, allowed value {1, 2}
	IrqCoresTunedNumMaxEachTime int // max number of irq cores whose affinitied irqs are permitted to tuned to other cores in each time, allowed value {1,2}
}

// when irq cores average cpu util greater-equal IrqCoresAvgCpuUtilThresh, then increase irq cores,
// when there are one or more irq cores's net load greater-equal IrqCoreNetOverloadThresholds, and failed to tune to irq load balance,
// then increase irq cores.
type IrqCoresIncThresholds struct {
	IrqCoresAvgCpuUtilThresh int // threshold of increasing irq cores, generally this thresh equal to or a litter greater-than IrqCoresExpectedCpuUtil
}

// when irq cores cpu util nearly full(e.g., greater-equal 85%), in order to reduce the impact time on the applications, it is necessary to immediately
// fallback to the balance-fair policy first, and later irq tuning manager will auto switch back to IrqCoresExclusive policy based on policies and conditions.
type IrqCoresIncConfig struct {
	SuccessiveIncInterval int // interval of two successive irq cores increase MUST greater-equal this interval
	IrqCoresCpuFullThresh int // when irq cores cpu util hit this thresh, then fallback to balance-fair policy
	Thresholds            IrqCoresIncThresholds
}

// when irq cores average cpu util less-equal IrqCoresAvgCpuUtilThresh, then decrease irq cores.
type IrqCoresDecThresholds struct {
	IrqCoresAvgCpuUtilThresh int // threshold of decreasing irq cores, generally this thresh should be less-than IrqCoresExpectedCpuUtil
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

// when successive count of nic's total PPS >= RxPPSThresh is greater-equal SuccessiveCount,, then enable exclusion of this nic's irq cores.
type EnableIrqCoresExclusionThresholds struct {
	RxPPSThresh     uint64
	SuccessiveCount int
}

// when successive count of nic's total PPS <= RxPPSThresh is greater-equal SuccessiveCount, then disable exclusion of this nic's irq cores.
type DisableIrqCoresExclusionThresholds struct {
	RxPPSThresh     uint64
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

// Configuration for irq-tuning
type IrqTuningConfig struct {
	Interval                 int
	EnableIrqTuning          bool
	IrqTuningPolicy          IrqTuningPolicy
	EnableRPS                bool                     // enable rps according to machine specifications configured by kcc, only balance-fair policy support enable rps
	EnableRPSCPUVSNicsQueue  float64                  // enable rps when (cpus count)/(nics queue count) greater than this config
	NicAffinitySocketsPolicy NicAffinitySocketsPolicy // nics's irqs affinity sockets policy
	IrqCoresExpectedCpuUtil  int
	ThrouputClassSwitchConf  ThroughputClassSwitchConfig
	ReniceIrqCoresKsoftirqd  bool
	IrqCoresKsoftirqdNice    int
	IrqCoreNetOverLoadThresh IrqCoreNetOverloadThresholds
	IrqLoadBalanceConf       IrqLoadBalanceConfig
	IrqCoresAdjustConf       IrqCoresAdjustConfig
	IrqCoresExclusionConf    IrqCoresExclusionConfig
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
		ThrouputClassSwitchConf: ThroughputClassSwitchConfig{
			LowThroughputThresholds: LowThroughputThresholds{
				RxPPSThresh:     3000,
				SuccessiveCount: 30,
			},
			NormalThroughputThresholds: NormalThroughputThresholds{
				RxPPSThresh:     6000,
				SuccessiveCount: 10,
			},
		},
		ReniceIrqCoresKsoftirqd: false,
		IrqCoresKsoftirqdNice:   -20,
		IrqCoreNetOverLoadThresh: IrqCoreNetOverloadThresholds{
			IrqCoreSoftNetTimeSqueezeRatio: 0.1,
		},
		IrqLoadBalanceConf: IrqLoadBalanceConfig{
			SuccessiveTuningInterval: 10,
			Thresholds: IrqLoadBalanceTuningThresholds{
				IrqCoreCpuUtilThresh:    65,
				IrqCoreCpuUtilGapThresh: 20,
			},
			PingPongIntervalThresh:      180,
			PingPongCountThresh:         1,
			IrqsTunedNumMaxEachTime:     2,
			IrqCoresTunedNumMaxEachTime: 1,
		},
		IrqCoresAdjustConf: IrqCoresAdjustConfig{
			IrqCoresPercentMin: 2,
			IrqCoresPercentMax: 30,
			IrqCoresIncConf: IrqCoresIncConfig{
				SuccessiveIncInterval: 5,
				IrqCoresCpuFullThresh: 85,
				Thresholds: IrqCoresIncThresholds{
					IrqCoresAvgCpuUtilThresh: 60,
				},
			},
			IrqCoresDecConf: IrqCoresDecConfig{
				SuccessiveDecInterval:    30,
				PingPongAdjustInterval:   300,
				SinceLastBalanceInterval: 60,
				Thresholds: IrqCoresDecThresholds{
					IrqCoresAvgCpuUtilThresh: 40,
				},
				DecCoresMaxEachTime: 1,
			},
		},
		IrqCoresExclusionConf: IrqCoresExclusionConfig{
			Thresholds: IrqCoresExclusionThresholds{
				EnableThresholds: EnableIrqCoresExclusionThresholds{
					RxPPSThresh:     60000,
					SuccessiveCount: 30,
				},
				DisableThresholds: DisableIrqCoresExclusionThresholds{
					RxPPSThresh:     30000,
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
	msg = fmt.Sprintf("%s    ThrouputClassSwitchConf:\n", msg)
	msg = fmt.Sprintf("%s        LowThroughputThresholds:\n", msg)
	msg = fmt.Sprintf("%s            RxPPSThresh: %d\n", msg, c.ThrouputClassSwitchConf.LowThroughputThresholds.RxPPSThresh)
	msg = fmt.Sprintf("%s            SuccessiveCount: %d\n", msg, c.ThrouputClassSwitchConf.LowThroughputThresholds.SuccessiveCount)
	msg = fmt.Sprintf("%s        NormalThroughputThresholds:\n", msg)
	msg = fmt.Sprintf("%s            RxPPSThresh: %d\n", msg, c.ThrouputClassSwitchConf.NormalThroughputThresholds.RxPPSThresh)
	msg = fmt.Sprintf("%s            SuccessiveCount: %d\n", msg, c.ThrouputClassSwitchConf.NormalThroughputThresholds.SuccessiveCount)
	msg = fmt.Sprintf("%s    ReniceIrqCoresKsoftirqd: %t\n", msg, c.ReniceIrqCoresKsoftirqd)
	msg = fmt.Sprintf("%s    IrqCoresKsoftirqdNice: %d\n", msg, c.IrqCoresKsoftirqdNice)
	msg = fmt.Sprintf("%s    IrqCoreNetOverLoadThresh:\n", msg)
	msg = fmt.Sprintf("%s        IrqCoreSoftNetTimeSqueezeRatio: %f\n", msg, c.IrqCoreNetOverLoadThresh.IrqCoreSoftNetTimeSqueezeRatio)
	msg = fmt.Sprintf("%s    IrqLoadBalanceConf:\n", msg)
	msg = fmt.Sprintf("%s        SuccessiveTuningInterval: %d\n", msg, c.IrqLoadBalanceConf.SuccessiveTuningInterval)
	msg = fmt.Sprintf("%s        Thresholds:\n", msg)
	msg = fmt.Sprintf("%s            IrqCoreCpuUtilThresh: %d\n", msg, c.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilThresh)
	msg = fmt.Sprintf("%s            IrqCoreCpuUtilGapThresh: %d\n", msg, c.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilGapThresh)
	msg = fmt.Sprintf("%s        PingPongIntervalThresh: %d\n", msg, c.IrqLoadBalanceConf.PingPongIntervalThresh)
	msg = fmt.Sprintf("%s        PingPongCountThresh: %d\n", msg, c.IrqLoadBalanceConf.PingPongCountThresh)
	msg = fmt.Sprintf("%s        IrqsTunedNumMaxEachTime: %d\n", msg, c.IrqLoadBalanceConf.IrqsTunedNumMaxEachTime)
	msg = fmt.Sprintf("%s        IrqCoresTunedNumMaxEachTime: %d\n", msg, c.IrqLoadBalanceConf.IrqCoresTunedNumMaxEachTime)
	msg = fmt.Sprintf("%s    IrqCoresAdjustConf:\n", msg)
	msg = fmt.Sprintf("%s        IrqCoresPercentMin: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresPercentMin)
	msg = fmt.Sprintf("%s        IrqCoresPercentMax: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresPercentMax)
	msg = fmt.Sprintf("%s        IrqCoresIncConf:\n", msg)
	msg = fmt.Sprintf("%s            SuccessiveIncInterval: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresIncConf.SuccessiveIncInterval)
	msg = fmt.Sprintf("%s            IrqCoresCpuFullThresh: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresIncConf.IrqCoresCpuFullThresh)
	msg = fmt.Sprintf("%s            Thresholds\n", msg)
	msg = fmt.Sprintf("%s                IrqCoresAvgCpuUtilThresh: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresIncConf.Thresholds.IrqCoresAvgCpuUtilThresh)
	msg = fmt.Sprintf("%s        IrqCoresDecConf:\n", msg)
	msg = fmt.Sprintf("%s            SuccessiveDecInterval: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.SuccessiveDecInterval)
	msg = fmt.Sprintf("%s            PingPongAdjustInterval: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.PingPongAdjustInterval)
	msg = fmt.Sprintf("%s            SinceLastBalanceInterval: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.SinceLastBalanceInterval)
	msg = fmt.Sprintf("%s            Thresholds\n", msg)
	msg = fmt.Sprintf("%s                IrqCoresAvgCpuUtilThresh: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.Thresholds.IrqCoresAvgCpuUtilThresh)
	msg = fmt.Sprintf("%s            DecCoresMaxEachTime: %d\n", msg, c.IrqCoresAdjustConf.IrqCoresDecConf.DecCoresMaxEachTime)
	msg = fmt.Sprintf("%s    IrqCoresExclusionConf:\n", msg)
	msg = fmt.Sprintf("%s        Thresholds:\n", msg)
	msg = fmt.Sprintf("%s            EnableThresholds:\n", msg)
	msg = fmt.Sprintf("%s                RxPPSThresh: %d\n", msg, c.IrqCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThresh)
	msg = fmt.Sprintf("%s                SuccessiveCount: %d\n", msg, c.IrqCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount)
	msg = fmt.Sprintf("%s            DisableThresholds:\n", msg)
	msg = fmt.Sprintf("%s                RxPPSThresh: %d\n", msg, c.IrqCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThresh)
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

	if c.ThrouputClassSwitchConf.LowThroughputThresholds.RxPPSThresh != other.ThrouputClassSwitchConf.LowThroughputThresholds.RxPPSThresh ||
		c.ThrouputClassSwitchConf.LowThroughputThresholds.SuccessiveCount != other.ThrouputClassSwitchConf.LowThroughputThresholds.SuccessiveCount ||
		c.ThrouputClassSwitchConf.NormalThroughputThresholds.RxPPSThresh != other.ThrouputClassSwitchConf.NormalThroughputThresholds.RxPPSThresh ||
		c.ThrouputClassSwitchConf.NormalThroughputThresholds.SuccessiveCount != other.ThrouputClassSwitchConf.NormalThroughputThresholds.SuccessiveCount {
		return false
	}

	if c.ReniceIrqCoresKsoftirqd != other.ReniceIrqCoresKsoftirqd ||
		c.IrqCoresKsoftirqdNice != other.IrqCoresKsoftirqdNice {
		return false
	}

	if c.IrqCoreNetOverLoadThresh.IrqCoreSoftNetTimeSqueezeRatio != other.IrqCoreNetOverLoadThresh.IrqCoreSoftNetTimeSqueezeRatio {
		return false
	}

	if c.IrqLoadBalanceConf.SuccessiveTuningInterval != other.IrqLoadBalanceConf.SuccessiveTuningInterval ||
		c.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilThresh != other.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilThresh ||
		c.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilGapThresh != other.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilGapThresh ||
		c.IrqLoadBalanceConf.PingPongIntervalThresh != other.IrqLoadBalanceConf.PingPongIntervalThresh ||
		c.IrqLoadBalanceConf.PingPongCountThresh != other.IrqLoadBalanceConf.PingPongCountThresh ||
		c.IrqLoadBalanceConf.IrqsTunedNumMaxEachTime != other.IrqLoadBalanceConf.IrqsTunedNumMaxEachTime ||
		c.IrqLoadBalanceConf.IrqCoresTunedNumMaxEachTime != other.IrqLoadBalanceConf.IrqCoresTunedNumMaxEachTime {
		return false
	}

	if c.IrqCoresAdjustConf.IrqCoresPercentMin != other.IrqCoresAdjustConf.IrqCoresPercentMin ||
		c.IrqCoresAdjustConf.IrqCoresPercentMax != other.IrqCoresAdjustConf.IrqCoresPercentMax ||
		c.IrqCoresAdjustConf.IrqCoresIncConf.SuccessiveIncInterval != other.IrqCoresAdjustConf.IrqCoresIncConf.SuccessiveIncInterval ||
		c.IrqCoresAdjustConf.IrqCoresIncConf.IrqCoresCpuFullThresh != other.IrqCoresAdjustConf.IrqCoresIncConf.IrqCoresCpuFullThresh ||
		c.IrqCoresAdjustConf.IrqCoresIncConf.Thresholds.IrqCoresAvgCpuUtilThresh != other.IrqCoresAdjustConf.IrqCoresIncConf.Thresholds.IrqCoresAvgCpuUtilThresh ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.SuccessiveDecInterval != other.IrqCoresAdjustConf.IrqCoresDecConf.SuccessiveDecInterval ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.PingPongAdjustInterval != other.IrqCoresAdjustConf.IrqCoresDecConf.PingPongAdjustInterval ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.SinceLastBalanceInterval != other.IrqCoresAdjustConf.IrqCoresDecConf.SinceLastBalanceInterval ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.Thresholds.IrqCoresAvgCpuUtilThresh != other.IrqCoresAdjustConf.IrqCoresDecConf.Thresholds.IrqCoresAvgCpuUtilThresh ||
		c.IrqCoresAdjustConf.IrqCoresDecConf.DecCoresMaxEachTime != other.IrqCoresAdjustConf.IrqCoresDecConf.DecCoresMaxEachTime {
		return false
	}

	if c.IrqCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThresh != other.IrqCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThresh ||
		c.IrqCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount != other.IrqCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount ||
		c.IrqCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThresh != other.IrqCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThresh ||
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

	if conf.TuningInterval < IrqTuingIntervalMin {
		return fmt.Errorf("invalid TuningInterval: %d, less-than IrqTuingMinInterval: %d", conf.TuningInterval, IrqTuingIntervalMin)
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

	if conf.ThrouputClassSwitchConf != nil {
		if conf.ThrouputClassSwitchConf.LowThresholdConfig != nil {
			if conf.ThrouputClassSwitchConf.LowThresholdConfig.SuccessiveCount <= 0 {
				return fmt.Errorf("invalid ThrouputClassSwitchConf.LowThresholdConfig.SuccessiveCount: %d", conf.ThrouputClassSwitchConf.LowThresholdConfig.SuccessiveCount)
			}
		}

		if conf.ThrouputClassSwitchConf.NormalThresholdConfig != nil {
			if conf.ThrouputClassSwitchConf.NormalThresholdConfig.SuccessiveCount <= 0 {
				return fmt.Errorf("invalid NormalThresholdConfig.LowThresholdConfig.SuccessiveCount: %d", conf.ThrouputClassSwitchConf.NormalThresholdConfig.SuccessiveCount)
			}
		}

		if conf.ThrouputClassSwitchConf.LowThresholdConfig != nil &&
			conf.ThrouputClassSwitchConf.NormalThresholdConfig != nil {
			if conf.ThrouputClassSwitchConf.LowThresholdConfig.RxPPSThresh >= conf.ThrouputClassSwitchConf.NormalThresholdConfig.RxPPSThresh {
				return fmt.Errorf("ThrouputClassSwitchConf.LowThresholdConfig.RxPPSThresh: %d greater-equal ThrouputClassSwitchConf.NormalThresholdConfig.RxPPSThresh: %d",
					conf.ThrouputClassSwitchConf.LowThresholdConfig.RxPPSThresh, conf.ThrouputClassSwitchConf.NormalThresholdConfig.RxPPSThresh)
			}
		}
	}

	if conf.LoadBalanceConf != nil {
		lbConf := conf.LoadBalanceConf

		if lbConf.SuccessiveTuningInterval < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.SuccessiveTuningInterval: %d", lbConf.SuccessiveTuningInterval)
		}

		if lbConf.PingPongIntervalThresh < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.PingPongIntervalThresh: %d", lbConf.PingPongIntervalThresh)
		}

		if lbConf.PingPongCountThresh < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.PingPongCountThresh: %d", lbConf.PingPongCountThresh)
		}

		if lbConf.IRQsTunedNumMaxEachTime < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.IRQsTunedNumMaxEachTime: %d", lbConf.IRQsTunedNumMaxEachTime)
		}

		if lbConf.IRQCoresTunedNumMaxEachTime < 1 {
			return fmt.Errorf("invalid LoadBalanceConf.IRQsTunedNumMaxEachTime: %d", lbConf.IRQCoresTunedNumMaxEachTime)
		}

		if lbConf.Thresholds != nil {
			if lbConf.Thresholds.CPUUtilThresh <= 0 || lbConf.Thresholds.CPUUtilThresh >= 100 {
				return fmt.Errorf("invalid LoadBalanceConf.Thresholds.CPUUtilThresh: %d", lbConf.Thresholds.CPUUtilThresh)
			}

			if lbConf.Thresholds.CPUUtilThresh <= conf.CoresExpectedCPUUtil {
				return fmt.Errorf("LoadBalanceConf.Thresholds.CPUUtilThresh %d less-equal CoresExpectedCPUUtil: %d",
					lbConf.Thresholds.CPUUtilThresh, conf.CoresExpectedCPUUtil)
			}

			if conf.CoresAdjustConf != nil && conf.CoresAdjustConf.IncConf != nil && conf.CoresAdjustConf.IncConf.Thresholds != nil {
				// LoadBalanceConf.Thresholds.CPUUtilThresh (threshold of balancing irqs) should greater than
				// CoresAdjustConf.IncConf.Thresholds.AvgCPUUtil (threshthreshold of increasing irq cores),
				// it's meaningless to let LoadBalanceConf.Thresholds.CPUUtilThresh less-equal CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh.
				if lbConf.Thresholds.CPUUtilThresh <= conf.CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh {
					return fmt.Errorf("LoadBalanceConf.Thresholds.CPUUtilThresh %d less-equal CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh %d",
						lbConf.Thresholds.CPUUtilThresh, conf.CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh)
				}
			}

			if lbConf.Thresholds.CPUUtilGapThresh <= 0 || lbConf.Thresholds.CPUUtilGapThresh >= 100 {
				return fmt.Errorf("invalid LoadBalanceConf.Thresholds.CPUUtilGapThresh: %d", lbConf.Thresholds.CPUUtilGapThresh)
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

			if incConf.FullThresh <= 0 || incConf.FullThresh > 100 {
				return fmt.Errorf("CoresAdjustConf.IncConf.FullThresh: %d", incConf.FullThresh)
			}

			if incConf.Thresholds != nil {
				if incConf.FullThresh <= incConf.Thresholds.AvgCPUUtilThresh {
					return fmt.Errorf("CoresAdjustConf.IncConf.FullThresh %d less-equal CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh %d",
						incConf.FullThresh, incConf.Thresholds.AvgCPUUtilThresh)
				}

				if incConf.Thresholds.AvgCPUUtilThresh <= 0 || incConf.Thresholds.AvgCPUUtilThresh >= 100 {
					return fmt.Errorf("invalid CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh %d", incConf.Thresholds.AvgCPUUtilThresh)
				}

				if incConf.Thresholds.AvgCPUUtilThresh <= conf.CoresExpectedCPUUtil {
					return fmt.Errorf("CoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh %d less-equal CoresExpectedCPUUtil %d",
						incConf.Thresholds.AvgCPUUtilThresh, conf.CoresExpectedCPUUtil)
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
				if decConf.Thresholds.AvgCPUUtilThresh <= 0 || decConf.Thresholds.AvgCPUUtilThresh >= 100 {
					return fmt.Errorf("invalid CoresAdjustConf.DecConf.Thresholds.AvgCPUUtilThresh: %d", decConf.Thresholds.AvgCPUUtilThresh)
				}

				if decConf.Thresholds.AvgCPUUtilThresh >= conf.CoresExpectedCPUUtil {
					return fmt.Errorf("CoresAdjustConf.DecConf.Thresholds.AvgCPUUtilThresh %d greater-equal CoresExpectedCPUUtil %d",
						decConf.Thresholds.AvgCPUUtilThresh, conf.CoresExpectedCPUUtil)
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
				if exclConf.Thresholds.DisableThresholds.RxPPSThresh >= exclConf.Thresholds.EnableThresholds.RxPPSThresh {
					return fmt.Errorf("CoresExclusionConf.Thresholds.DisableThresholds.RxPPSThresh: %d greater-equal CoresExclusionConf.Thresholds.EnableThresholds.RxPPSThresh: %d",
						exclConf.Thresholds.DisableThresholds.RxPPSThresh, exclConf.Thresholds.EnableThresholds.RxPPSThresh)
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

		if dynamicConf.IRQTuningConfiguration.ThrouputClassSwitchConf != nil {
			throughputClassSwitchConf := dynamicConf.IRQTuningConfiguration.ThrouputClassSwitchConf
			if throughputClassSwitchConf.LowThresholdConfig != nil {
				conf.ThrouputClassSwitchConf.LowThroughputThresholds.RxPPSThresh = throughputClassSwitchConf.LowThresholdConfig.RxPPSThresh
				conf.ThrouputClassSwitchConf.LowThroughputThresholds.SuccessiveCount = throughputClassSwitchConf.LowThresholdConfig.SuccessiveCount
			}

			if throughputClassSwitchConf.NormalThresholdConfig != nil {
				conf.ThrouputClassSwitchConf.NormalThroughputThresholds.RxPPSThresh = throughputClassSwitchConf.NormalThresholdConfig.RxPPSThresh
				conf.ThrouputClassSwitchConf.NormalThroughputThresholds.SuccessiveCount = throughputClassSwitchConf.NormalThresholdConfig.SuccessiveCount
			}
		}

		conf.ReniceIrqCoresKsoftirqd = dynamicConf.IRQTuningConfiguration.ReniceKsoftirqd
		conf.IrqCoresKsoftirqdNice = dynamicConf.IRQTuningConfiguration.KsoftirqdNice

		if dynamicConf.IRQTuningConfiguration.CoreNetOverLoadThresh != nil {
			conf.IrqCoreNetOverLoadThresh.IrqCoreSoftNetTimeSqueezeRatio = dynamicConf.IRQTuningConfiguration.CoreNetOverLoadThresh.SoftNetTimeSqueezeRatio
		}

		if dynamicConf.IRQTuningConfiguration.LoadBalanceConf != nil {
			dynLoadBalanceConf := dynamicConf.IRQTuningConfiguration.LoadBalanceConf
			conf.IrqLoadBalanceConf.SuccessiveTuningInterval = dynLoadBalanceConf.SuccessiveTuningInterval
			if dynLoadBalanceConf.Thresholds != nil {
				conf.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilThresh = dynLoadBalanceConf.Thresholds.CPUUtilThresh
				conf.IrqLoadBalanceConf.Thresholds.IrqCoreCpuUtilGapThresh = dynLoadBalanceConf.Thresholds.CPUUtilGapThresh
			}
			conf.IrqLoadBalanceConf.PingPongIntervalThresh = dynLoadBalanceConf.PingPongIntervalThresh
			conf.IrqLoadBalanceConf.PingPongCountThresh = dynLoadBalanceConf.PingPongCountThresh
			conf.IrqLoadBalanceConf.IrqsTunedNumMaxEachTime = dynLoadBalanceConf.IRQsTunedNumMaxEachTime
			conf.IrqLoadBalanceConf.IrqCoresTunedNumMaxEachTime = dynLoadBalanceConf.IRQCoresTunedNumMaxEachTime
		}

		if dynamicConf.IRQTuningConfiguration.CoresAdjustConf != nil {
			dynCoresAdjustConf := dynamicConf.IRQTuningConfiguration.CoresAdjustConf
			conf.IrqCoresAdjustConf.IrqCoresPercentMin = dynCoresAdjustConf.PercentMin
			conf.IrqCoresAdjustConf.IrqCoresPercentMax = dynCoresAdjustConf.PercentMax

			if dynCoresAdjustConf.IncConf != nil {
				conf.IrqCoresAdjustConf.IrqCoresIncConf.SuccessiveIncInterval = dynCoresAdjustConf.IncConf.SuccessiveIncInterval
				conf.IrqCoresAdjustConf.IrqCoresIncConf.IrqCoresCpuFullThresh = dynCoresAdjustConf.IncConf.FullThresh
				if dynCoresAdjustConf.IncConf.Thresholds != nil {
					conf.IrqCoresAdjustConf.IrqCoresIncConf.Thresholds.IrqCoresAvgCpuUtilThresh = dynCoresAdjustConf.IncConf.Thresholds.AvgCPUUtilThresh
				}
			}

			if dynCoresAdjustConf.DecConf != nil {
				conf.IrqCoresAdjustConf.IrqCoresDecConf.SuccessiveDecInterval = dynCoresAdjustConf.DecConf.SuccessiveDecInterval
				conf.IrqCoresAdjustConf.IrqCoresDecConf.PingPongAdjustInterval = dynCoresAdjustConf.DecConf.PingPongAdjustInterval
				conf.IrqCoresAdjustConf.IrqCoresDecConf.SinceLastBalanceInterval = dynCoresAdjustConf.DecConf.SinceLastBalanceInterval
				if dynCoresAdjustConf.DecConf.Thresholds != nil {
					conf.IrqCoresAdjustConf.IrqCoresDecConf.Thresholds.IrqCoresAvgCpuUtilThresh = dynCoresAdjustConf.DecConf.Thresholds.AvgCPUUtilThresh
				}
				conf.IrqCoresAdjustConf.IrqCoresDecConf.DecCoresMaxEachTime = dynCoresAdjustConf.DecConf.DecCoresMaxEachTime
			}
		}

		if dynamicConf.IRQTuningConfiguration.CoresExclusionConf != nil {
			dynCoresExclusionConf := dynamicConf.IRQTuningConfiguration.CoresExclusionConf
			if dynCoresExclusionConf.Thresholds != nil {
				if dynCoresExclusionConf.Thresholds.EnableThresholds != nil {
					conf.IrqCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThresh = dynCoresExclusionConf.Thresholds.EnableThresholds.RxPPSThresh
					conf.IrqCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount = dynCoresExclusionConf.Thresholds.EnableThresholds.SuccessiveCount
				}
				if dynCoresExclusionConf.Thresholds.DisableThresholds != nil {
					conf.IrqCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThresh = dynCoresExclusionConf.Thresholds.DisableThresholds.RxPPSThresh
					conf.IrqCoresExclusionConf.Thresholds.DisableThresholds.SuccessiveCount = dynCoresExclusionConf.Thresholds.DisableThresholds.SuccessiveCount
				}
				conf.IrqCoresExclusionConf.SuccessiveSwitchInterval = dynCoresExclusionConf.SuccessiveSwitchInterval
			}
		}
	}

	return conf
}
