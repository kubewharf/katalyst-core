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
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

type SystemLoadPressureEvictionOptions struct {
	SoftThreshold          int64
	HardThreshold          int64
	HistorySize            int64
	SyncPeriod             int64
	CoolDownTime           int64
	GracePeriod            int64
	ThresholdMetPercentage float64
}

func NewSystemLoadPressureEvictionOptions() *SystemLoadPressureEvictionOptions {
	return &SystemLoadPressureEvictionOptions{
		SoftThreshold:          500,
		HardThreshold:          600,
		HistorySize:            10,
		SyncPeriod:             30,
		CoolDownTime:           300,
		GracePeriod:            30,
		ThresholdMetPercentage: 0.8,
	}
}

func (s *SystemLoadPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-system-load-pressure")

	fs.Int64Var(&s.SoftThreshold, "system-load-pressure-soft-threshold", s.SoftThreshold, "the soft threshold of "+
		"load pressure, it should be the value of n * 100 which stands for the n * core_number")
	fs.Int64Var(&s.HardThreshold, "system-load-pressure-hard-threshold", s.HardThreshold, "the hard threshold of "+
		"load pressure, it should be the value of n * 100 which stands for the n * core_number")
	fs.Int64Var(&s.HistorySize, "system-load-pressure-history-size", s.HistorySize, "how many latest load records "+
		"the plugin will store")
	fs.Int64Var(&s.SyncPeriod, "system-load-pressure-sync-period", s.SyncPeriod, "the interval of the plugin fetch "+
		"the load information")
	fs.Int64Var(&s.CoolDownTime, "system-load-pressure-cool-down-period", s.CoolDownTime, "the cool down"+
		"time of the plugin evict pods")
	fs.Int64Var(&s.GracePeriod, "system-load-pressure-evict-grace-period", s.GracePeriod, "the grace"+
		"period of pod deletion")
	fs.Float64Var(&s.ThresholdMetPercentage, "system-load-pressure-eviction-threshold-met-percentage", s.ThresholdMetPercentage,
		"when the ratio of load history which is greater than threshold is greater than this percentage, it met the threshold")
}

func (s *SystemLoadPressureEvictionOptions) ApplyTo(c *eviction.SystemLoadEvictionPluginConfiguration) error {
	c.SoftThreshold = s.SoftThreshold
	c.HardThreshold = s.HardThreshold
	c.HistorySize = s.HistorySize
	c.SyncPeriod = s.SyncPeriod
	c.CoolDownTime = s.CoolDownTime
	c.GracePeriod = s.GracePeriod
	c.ThresholdMetPercentage = s.ThresholdMetPercentage
	return nil
}
