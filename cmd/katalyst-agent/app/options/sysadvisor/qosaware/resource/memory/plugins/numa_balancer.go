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

package plugins

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/resource/memory/plugins"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type NumaBalancerOptions struct {
	ReadLatencyMetricName map[string]string

	GraceBalanceReadLatencyThreshold map[string]int64
	ForceBalanceReadLatencyThreshold map[string]int64

	GraceBalanceReadLatencyGapThreshold map[string]int64
	GraceBalanceNumaDistanceMax         int64

	GraceBalanceGapRatio float64
	ForceBalanceGapRatio float64

	BalancedPodSourceNumaRSSMax int64
	BalancedPodSourceNumaRSSMin int64

	BalancedReclaimedPodSourceNumaRSSMax int64
	BalancedReclaimedPodSourceNumaRSSMin int64

	BalancedPodsSourceNumaTotalRSSMax          int64
	BalancedReclaimedPodsSourceNumaTotalRSSMax int64

	BalancedReclaimedPodsSingleRoundTotalRSSThreshold int64

	SupportedPools []string
}

func NewNumaBalancerOptions() *NumaBalancerOptions {
	return &NumaBalancerOptions{
		ReadLatencyMetricName:            map[string]string{util.DefaultConfigKey: consts.MetricMemLatencyReadNuma, util.CPUVendorAMD: consts.MetricMemAMDL3MissLatencyNuma},
		GraceBalanceReadLatencyThreshold: map[string]int64{util.DefaultConfigKey: 70, util.CPUVendorAMD: 500},
		ForceBalanceReadLatencyThreshold: map[string]int64{util.DefaultConfigKey: 80, util.CPUVendorAMD: 600},

		// when the gap between the max latency and the min latency of conditional nodes greater than this threshold,
		// then trigger grace balance,
		// conditions which conditional nodes must meet:
		//   1) distance from the node which has the max latency is less-euqal GraceBalanceNumaDistanceMax
		//   2) which are nearest to the node which has the max latency, even theoretically maybe there are
		//      multiple nodes distances less-equal GraceBalanceNumaDistanceMax
		GraceBalanceReadLatencyGapThreshold: map[string]int64{util.DefaultConfigKey: 40, util.CPUVendorAMD: 400},
		GraceBalanceNumaDistanceMax:         19,

		GraceBalanceGapRatio: 0.15,
		ForceBalanceGapRatio: 0.2,

		BalancedPodSourceNumaRSSMax: 10 * 1024 * 1024 * 1024,
		BalancedPodSourceNumaRSSMin: 128 * 1024 * 1024,

		BalancedReclaimedPodSourceNumaRSSMax: 15 * 1024 * 1024 * 1024,
		BalancedReclaimedPodSourceNumaRSSMin: 128 * 1024 * 1024,

		BalancedPodsSourceNumaTotalRSSMax:          12 * 1024 * 1024 * 1024,
		BalancedReclaimedPodsSourceNumaTotalRSSMax: 15 * 1024 * 1024 * 1024,

		BalancedReclaimedPodsSingleRoundTotalRSSThreshold: 3 * 1024 * 1024 * 1024,

		SupportedPools: []string{state.PoolNameShare},
	}
}

func (o *NumaBalancerOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringToStringVar(&o.ReadLatencyMetricName, "read-latency-metric-name", o.ReadLatencyMetricName,
		"the metric which measures the memory read latency, vendor aware format. e.g. \"Intel:MemReadLatencyMax,AMD:AmdL3MissLatencyMax\"")
	fs.StringToInt64Var(&o.GraceBalanceReadLatencyThreshold, "grace-balance-read-latency-threshold", o.GraceBalanceReadLatencyThreshold,
		"the read latency threshold whether to launch grace balance, vendor aware format")
	fs.StringToInt64Var(&o.ForceBalanceReadLatencyThreshold, "force-balance-read-latency-threshold", o.ForceBalanceReadLatencyThreshold,
		"the read latency threshold whether to launch force balance, vendor aware format")
	fs.StringToInt64Var(&o.GraceBalanceReadLatencyGapThreshold, "grace-balance-read-latency-gap-threshold", o.GraceBalanceReadLatencyGapThreshold,
		"the read latency gap threshold whether to launch a grace balance when two neighboring NUMAs has big read latency gap, vendor aware format")
	fs.Int64Var(&o.GraceBalanceNumaDistanceMax, "grace-balance-numa-distance-max", o.GraceBalanceNumaDistanceMax,
		"the distance threshold between two NUMAs, the grace balance only happens between two NUMAs whose distance is lesser than this threshold")
	fs.Float64Var(&o.GraceBalanceGapRatio, "grace-balance-gap-ratio", o.GraceBalanceGapRatio,
		"the read latency gap ratio threshold, the grace balance only happens between two NUMAs whose read latency gap is greater than this threshold")
	fs.Float64Var(&o.ForceBalanceGapRatio, "force-balance-gap-ratio", o.ForceBalanceGapRatio,
		"the read latency gap ratio threshold, the force balance only happens between two NUMAs whose read latency gap is greater than this threshold")
	fs.Int64Var(&o.BalancedPodSourceNumaRSSMax, "balanced-pod-source-numa-rss-max", o.BalancedPodSourceNumaRSSMax,
		"the rss upper limit on source numa of a single non-reclaimed pod whose memory will be migrated to relief the source numa memory bandwidth pressure")
	fs.Int64Var(&o.BalancedPodSourceNumaRSSMin, "balanced-pod-source-numa-rss-min", o.BalancedPodSourceNumaRSSMin,
		"the rss lower limit on source numa of a single non-reclaimed pod whose memory will be migrated to relief the source numa memory bandwidth pressure")
	fs.Int64Var(&o.BalancedReclaimedPodSourceNumaRSSMax, "balanced-reclaimed-pod-source-numa-rss-max", o.BalancedReclaimedPodSourceNumaRSSMax,
		"the rss upper limit on source numa of a single reclaimed pod whose memory will be migrated to relief the source numa memory bandwidth pressure")
	fs.Int64Var(&o.BalancedReclaimedPodSourceNumaRSSMin, "balanced-reclaimed-pod-source-numa-rss-min", o.BalancedReclaimedPodSourceNumaRSSMin,
		"the rss lower limit on source numa of a single reclaimed pod whose memory will be migrated to relief the source numa memory bandwidth pressure")
	fs.Int64Var(&o.BalancedPodsSourceNumaTotalRSSMax, "balanced-pod-source-numa-total-rss-max", o.BalancedPodsSourceNumaTotalRSSMax,
		"the rss upper limit on source numa of all non-reclaimed pods whose memory will be migrated to relief the source numa memory bandwidth pressure")
	fs.Int64Var(&o.BalancedReclaimedPodsSourceNumaTotalRSSMax, "balanced-reclaimed-pod-source-numa-total-rss-max", o.BalancedReclaimedPodsSourceNumaTotalRSSMax,
		"the rss upper limit on source numa of all reclaimed pods whose memory will be migrated to relief the source numa memory bandwidth pressure")
	fs.Int64Var(&o.BalancedReclaimedPodsSingleRoundTotalRSSThreshold, "balanced-reclaimed-pod-single-round-total-rss-threshold", o.BalancedReclaimedPodsSingleRoundTotalRSSThreshold,
		"if the migrated amount of rss from reclaimed pods is over this threshold, no non-reclaimed pod's memory will be migrated")
	fs.StringSliceVar(&o.SupportedPools, "numa-balance-supported-pools", o.SupportedPools,
		"the pool list support numa memory balance, only pods in these pools will be balanced.")
}

func (o *NumaBalancerOptions) ApplyTo(c *plugins.NumaBalancerConfiguration) error {
	readLatencyMetricName, err := util.GetVendorAwareStringConfiguration(o.ReadLatencyMetricName)
	if err != nil {
		return err
	}
	c.ReadLatencyMetric = readLatencyMetricName

	graceBalanceReadLatencyThreshold, err := util.GetVendorAwareInt64Configuration(o.GraceBalanceReadLatencyThreshold)
	if err != nil {
		return err
	}
	c.GraceBalanceReadLatencyThreshold = float64(graceBalanceReadLatencyThreshold)

	forceBalanceReadLatencyThreshold, err := util.GetVendorAwareInt64Configuration(o.ForceBalanceReadLatencyThreshold)
	if err != nil {
		return err
	}
	c.ForceBalanceReadLatencyThreshold = float64(forceBalanceReadLatencyThreshold)

	graceBalanceReadLatencyGapThreshold, err := util.GetVendorAwareInt64Configuration(o.GraceBalanceReadLatencyGapThreshold)
	if err != nil {
		return err
	}
	c.GraceBalanceReadLatencyGapThreshold = float64(graceBalanceReadLatencyGapThreshold)

	c.GraceBalanceNumaDistanceMax = o.GraceBalanceNumaDistanceMax
	c.GraceBalanceGapRatio = o.GraceBalanceGapRatio
	c.ForceBalanceGapRatio = o.ForceBalanceGapRatio
	c.BalancedPodSourceNumaRSSMax = o.BalancedPodSourceNumaRSSMax
	c.BalancedPodSourceNumaRSSMin = o.BalancedPodSourceNumaRSSMin
	c.BalancedReclaimedPodSourceNumaRSSMax = o.BalancedReclaimedPodSourceNumaRSSMax
	c.BalancedReclaimedPodSourceNumaRSSMin = o.BalancedReclaimedPodSourceNumaRSSMin
	c.BalancedPodsSourceNumaTotalRSSMax = o.BalancedPodsSourceNumaTotalRSSMax
	c.BalancedReclaimedPodsSourceNumaTotalRSSMax = o.BalancedReclaimedPodsSourceNumaTotalRSSMax
	c.BalancedReclaimedPodsSingleRoundTotalRssThreshold = o.BalancedReclaimedPodsSingleRoundTotalRSSThreshold
	c.SupportedPools = o.SupportedPools

	return nil
}
