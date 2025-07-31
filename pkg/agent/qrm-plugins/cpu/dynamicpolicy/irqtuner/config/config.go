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

func (c *IrqTuningConfig) Validate() error {
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
