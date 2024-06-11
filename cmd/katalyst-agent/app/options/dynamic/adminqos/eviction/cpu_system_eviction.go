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
	"encoding/json"
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

const (
	defaultEnableCPUSystemEviction              = false
	defaultSystemLoadUpperBoundRatio            = 2
	defaultSystemLoadLowerBoundRatio            = 1
	defaultSystemUsageUpperBoundRatio           = 0.8
	defaultSystemUsageLowerBoundRatio           = 0.6
	defaultThresholdMetPercentage               = 0.8
	defaultMetricRingSize                       = 10
	defaultEvictionCoolDownTime                 = 300 * time.Second
	defaultCheckCPUManager                      = false
	defaultCPUSystemPressureEvictionGracePeriod = -1
)

var defaultEvictionRankingMetrics = []string{eviction.FakeMetricQoSLevel, eviction.FakeMetricNativeQoSLevel, eviction.FakeMetricPriority}

type CPUSystemPressureEvictionOptions struct {
	EnableCPUSystemEviction    bool
	SystemLoadUpperBoundRatio  float64
	SystemLoadLowerBoundRatio  float64
	SystemUsageUpperBoundRatio float64
	SystemUsageLowerBoundRatio float64
	ThresholdMetPercentage     float64
	MetricRingSize             int
	EvictionCoolDownTime       time.Duration
	EvictionRankingMetrics     []string
	GracePeriod                int64
	CheckCPUManager            bool
	RankingLabels              StringToSlice
}

func NewCPUSystemPressureEvictionOptions() *CPUSystemPressureEvictionOptions {
	return &CPUSystemPressureEvictionOptions{
		EnableCPUSystemEviction:    defaultEnableCPUSystemEviction,
		SystemLoadUpperBoundRatio:  defaultSystemLoadUpperBoundRatio,
		SystemLoadLowerBoundRatio:  defaultSystemLoadLowerBoundRatio,
		SystemUsageUpperBoundRatio: defaultSystemUsageUpperBoundRatio,
		SystemUsageLowerBoundRatio: defaultSystemUsageLowerBoundRatio,
		ThresholdMetPercentage:     defaultThresholdMetPercentage,
		MetricRingSize:             defaultMetricRingSize,
		EvictionCoolDownTime:       defaultEvictionCoolDownTime,
		EvictionRankingMetrics:     defaultEvictionRankingMetrics,
		GracePeriod:                defaultCPUSystemPressureEvictionGracePeriod,
		CheckCPUManager:            defaultCheckCPUManager,
		RankingLabels:              map[string][]string{},
	}
}

func (o *CPUSystemPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-cpu-system")

	fs.BoolVar(&o.EnableCPUSystemEviction, "eviction-cpu-system-enable", o.EnableCPUSystemEviction,
		"set true to enable cpu system eviction")
	fs.Float64Var(&o.SystemLoadUpperBoundRatio, "eviction-cpu-system-load-upper-bound-ratio", o.SystemLoadUpperBoundRatio,
		"multiply node capacity by this ration to get the load upper bound. "+
			"if the load of the node is greater than the load upper bound repeatedly, the eviction will be triggered. "+
			"default 2.0")
	fs.Float64Var(&o.SystemLoadLowerBoundRatio, "eviction-cpu-system-load-lower-bound-ratio", o.SystemLoadLowerBoundRatio,
		"multiply node capacity by this ration to get the load lower bound. "+
			"if the load of the node is greater than the load lower bound repeatedly, node taint will be triggered. "+
			"default 1.0")
	fs.Float64Var(&o.SystemUsageUpperBoundRatio, "eviction-cpu-system-usage-upper-bound-ratio", o.SystemUsageUpperBoundRatio,
		"multiply node capacity by this ration to get the usage upper bound. "+
			"if the cpu usage of the node is greater than the usage upper bound repeatedly, the eviction will be triggered. "+
			"default 0.8")
	fs.Float64Var(&o.SystemUsageLowerBoundRatio, "eviction-cpu-system-usage-lower-bound-ratio", o.SystemUsageLowerBoundRatio,
		"multiply node capacity by this ration to get the usage lower bound. "+
			"if the cpu usage of the node is greater than the usage lower bound repeatedly, node taint will be triggered. "+
			"default 0.6")
	fs.Float64Var(&o.ThresholdMetPercentage, "eviction-cpu-system-threshold-met-percentage", o.ThresholdMetPercentage,
		"the ratio between the times metric value over the bound value and the metric ring size is greater than this percentage "+
			", the eviction or node taint will be triggered, default 0.8")
	fs.IntVar(&o.MetricRingSize, "eviction-cpu-system-metric-ring-size", o.MetricRingSize,
		"the size of the metric ring, which is used to cache and aggregate the metrics of the node, default 10")
	fs.DurationVar(&o.EvictionCoolDownTime, "eviction-cpu-system-cool-down-time", o.EvictionCoolDownTime,
		"the cool-down time of cpu system eviction, if the cpu system eviction is triggered, "+
			"the cpu system eviction will be disabled for the cool-down time")
	fs.StringSliceVar(&o.EvictionRankingMetrics, "eviction-cpu-system-ranking-metrics", o.EvictionRankingMetrics,
		"metrics for ranking active pods when GetTopEvictionPods")
	fs.Int64Var(&o.GracePeriod, "eviction-cpu-system-grace-period", o.GracePeriod,
		"grace period when evicting pod")
	fs.BoolVar(&o.CheckCPUManager, "eviction-cpu-system-check-cpumanager", o.CheckCPUManager,
		"set true to check kubelet CPUManager policy, if CPUManager is on, guaranteed pods will be filtered when collecting metrics and evicting pods")
	fs.Var(&o.RankingLabels, "eviction-cpu-system-ranking-labels", "custom ranking labels, The later label values in the array have a higher eviction precedence")
}

func (o *CPUSystemPressureEvictionOptions) ApplyTo(c *eviction.CPUSystemPressureEvictionPluginConfiguration) error {
	c.EnableCPUSystemEviction = o.EnableCPUSystemEviction
	c.SystemLoadUpperBoundRatio = o.SystemLoadUpperBoundRatio
	c.SystemLoadLowerBoundRatio = o.SystemLoadLowerBoundRatio
	c.SystemUsageUpperBoundRatio = o.SystemUsageUpperBoundRatio
	c.SystemUsageLowerBoundRatio = o.SystemUsageLowerBoundRatio
	c.ThresholdMetPercentage = o.ThresholdMetPercentage
	c.MetricRingSize = o.MetricRingSize
	c.EvictionCoolDownTime = o.EvictionCoolDownTime
	c.EvictionRankingMetrics = o.EvictionRankingMetrics
	c.GracePeriod = o.GracePeriod
	c.CheckCPUManager = o.CheckCPUManager
	c.RankingLabels = o.RankingLabels
	return nil
}

type StringToSlice map[string][]string

func (s *StringToSlice) String() string {
	res, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(res)
}

func (s *StringToSlice) Set(value string) error {
	err := json.Unmarshal([]byte(value), s)
	return err
}

func (s *StringToSlice) Type() string {
	return "stringToSlice"
}
