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

package qrm

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type MemoryOptions struct {
	PolicyName                                    string
	ReservedMemoryGB                              uint64
	SkipMemoryStateCorruption                     bool
	EnableSettingMemoryMigrate                    bool
	EnableMemoryAdvisor                           bool
	AdvisorGetAdviceInterval                      time.Duration
	ExtraControlKnobConfigFile                    string
	EnableOOMPriority                             bool
	OOMPriorityPinnedMapAbsPath                   string
	EnableNonBindingShareCoresMemoryResourceCheck bool
	EnableNUMAAllocationReactor                   bool
	NUMABindResultResourceAllocationAnnotationKey string

	SockMemOptions
	LogCacheOptions
	FragMemOptions
}

type SockMemOptions struct {
	EnableSettingSockMem bool
	// SetGlobalTCPMemRatio limits global max tcp memory usage.
	SetGlobalTCPMemRatio int
	// SetCgroupTCPMemLimitRatio limit cgroup max tcp memory usage.
	SetCgroupTCPMemRatio int
}

type LogCacheOptions struct {
	// EnableEvictingLogCache is used to enable evicting log cache by advise kernel to throw page cache for log files
	EnableEvictingLogCache bool
	// If the change value of the page cache between two operations exceeds this threshold, then increase the frequency of subsequent eviction operations.
	HighThreshold uint64
	// If the change value of the page cache between two operations is lower than this value, then slow down the frequency of subsequent eviction operations.
	LowThreshold uint64
	// The minimum time interval between two operations
	MinInterval time.Duration
	// The maximum time interval between two operations
	MaxInterval time.Duration
	// The file directory for evicting the log cache
	PathList []string
	// Keywords for recognizing log files
	FileFilters []string
}

type FragMemOptions struct {
	EnableSettingFragMem bool
	// SetMemFragScoreAsync sets the threashold of frag score for async memory compaction.
	// The async compaction behavior will be triggered while exceeding this score.
	SetMemFragScoreAsync int
}

func NewMemoryOptions() *MemoryOptions {
	return &MemoryOptions{
		PolicyName:                                    "dynamic",
		ReservedMemoryGB:                              0,
		SkipMemoryStateCorruption:                     false,
		EnableSettingMemoryMigrate:                    false,
		EnableMemoryAdvisor:                           false,
		AdvisorGetAdviceInterval:                      5 * time.Second,
		EnableOOMPriority:                             false,
		EnableNonBindingShareCoresMemoryResourceCheck: true,
		EnableNUMAAllocationReactor:                   false,
		NUMABindResultResourceAllocationAnnotationKey: consts.QRMResourceAnnotationKeyNUMABindResult,
		SockMemOptions: SockMemOptions{
			EnableSettingSockMem: false,
			SetGlobalTCPMemRatio: 20,  // default: 20% * {host total memory}
			SetCgroupTCPMemRatio: 100, // default: 100% * {cgroup memory}
		},
		LogCacheOptions: LogCacheOptions{
			EnableEvictingLogCache: false,
			HighThreshold:          30, // default: 30GB
			LowThreshold:           5,  // default: 5GB
			MinInterval:            time.Second * 600,
			MaxInterval:            time.Second * 60 * 60 * 2,
			PathList:               []string{},
			FileFilters:            []string{".*\\.log.*"},
		},
		FragMemOptions: FragMemOptions{
			EnableSettingFragMem: false,
			SetMemFragScoreAsync: 80,
		},
	}
}

func (o *MemoryOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("memory_resource_plugin")

	fs.StringVar(&o.PolicyName, "memory-resource-plugin-policy",
		o.PolicyName, "The policy memory resource plugin should use")
	fs.Uint64Var(&o.ReservedMemoryGB, "memory-resource-plugin-reserved",
		o.ReservedMemoryGB, "reserved memory(GB) for system agents")
	fs.BoolVar(&o.SkipMemoryStateCorruption, "skip-memory-state-corruption",
		o.SkipMemoryStateCorruption, "if set true, we will skip memory state corruption")
	fs.BoolVar(&o.EnableSettingMemoryMigrate, "enable-setting-memory-migrate",
		o.EnableSettingMemoryMigrate, "if set true, we will enable cpuset.memory_migrate for containers not numa_binding")
	fs.BoolVar(&o.EnableMemoryAdvisor, "memory-resource-plugin-advisor",
		o.EnableMemoryAdvisor, "Whether memory resource plugin should enable sys-advisor")
	fs.DurationVar(&o.AdvisorGetAdviceInterval, "memory-resource-plugin-advisor-interval",
		o.AdvisorGetAdviceInterval, "If memory advisor is enabled, this is the interval at which we get advice from sys-advisor")
	fs.StringVar(&o.ExtraControlKnobConfigFile, "memory-extra-control-knob-config-file",
		o.ExtraControlKnobConfigFile, "the absolute path of extra control knob config file")
	fs.BoolVar(&o.EnableOOMPriority, "enable-oom-priority",
		o.EnableOOMPriority, "if set true, we will enable oom priority enhancement")
	fs.BoolVar(&o.EnableNonBindingShareCoresMemoryResourceCheck, "enable-non-binding-share-cores-memory-resource-check",
		o.EnableNonBindingShareCoresMemoryResourceCheck, "enable the topology check for non-binding shares cores pods")
	fs.BoolVar(&o.EnableNUMAAllocationReactor, "enable-numa-allocation-reactor",
		o.EnableNUMAAllocationReactor, "enable numa allocation reactor for numa binding pods to patch pod numa binding result annotation")
	fs.StringVar(&o.NUMABindResultResourceAllocationAnnotationKey, "numa-bind-result-resource-allocation-annotation-key",
		o.NUMABindResultResourceAllocationAnnotationKey, "the key of numa bind result resource allocation annotation")
	fs.StringVar(&o.OOMPriorityPinnedMapAbsPath, "oom-priority-pinned-bpf-map-path",
		o.OOMPriorityPinnedMapAbsPath, "the absolute path of oom priority pinned bpf map")
	fs.BoolVar(&o.EnableSettingSockMem, "enable-setting-sockmem",
		o.EnableSettingSockMem, "if set true, we will limit tcpmem usage in cgroup and host level")
	fs.IntVar(&o.SetGlobalTCPMemRatio, "qrm-memory-global-tcpmem-ratio",
		o.SetGlobalTCPMemRatio, "limit global max tcp memory usage")
	fs.IntVar(&o.SetCgroupTCPMemRatio, "qrm-memory-cgroup-tcpmem-ratio",
		o.SetCgroupTCPMemRatio, "limit cgroup max tcp memory usage")
	fs.BoolVar(&o.EnableEvictingLogCache, "enable-evicting-logcache",
		o.EnableEvictingLogCache, "if set true, we will enable log cache eviction")
	fs.Uint64Var(&o.HighThreshold, "qrm-memory-logcache-high-threshold",
		o.HighThreshold, "high level of evicted cache memory(GB) for log files")
	fs.Uint64Var(&o.LowThreshold, "qrm-memory-logcache-low-threshold",
		o.LowThreshold, "low level of evicted cache memory(GB) for log files")
	fs.DurationVar(&o.MinInterval, "qrm-memory-logcache-min-interval", o.MinInterval,
		"the minimum interval for logcache eviction")
	fs.DurationVar(&o.MaxInterval, "qrm-memory-logcache-max-interval", o.MaxInterval,
		"the maximum interval for logcache eviction")
	fs.StringSliceVar(&o.PathList, "qrm-memory-logcache-path-list", o.PathList,
		"the absolute path list where files will be checked to evict page cache")
	fs.StringSliceVar(&o.FileFilters, "qrm-memory-logcache-file-filters",
		o.FileFilters, "string list to filter log files, default to *log*")
	fs.BoolVar(&o.EnableSettingFragMem, "enable-setting-mem-compaction",
		o.EnableSettingFragMem, "if set true, we will enable memory compaction related features")
	fs.IntVar(&o.SetMemFragScoreAsync, "qrm-memory-frag-score-async",
		o.SetMemFragScoreAsync, "set the threshold of frag score for async memory compaction")
}

func (o *MemoryOptions) ApplyTo(conf *qrmconfig.MemoryQRMPluginConfig) error {
	conf.PolicyName = o.PolicyName
	conf.ReservedMemoryGB = o.ReservedMemoryGB
	conf.SkipMemoryStateCorruption = o.SkipMemoryStateCorruption
	conf.EnableSettingMemoryMigrate = o.EnableSettingMemoryMigrate
	conf.EnableMemoryAdvisor = o.EnableMemoryAdvisor
	conf.GetAdviceInterval = o.AdvisorGetAdviceInterval
	conf.ExtraControlKnobConfigFile = o.ExtraControlKnobConfigFile
	conf.EnableOOMPriority = o.EnableOOMPriority
	conf.EnableNonBindingShareCoresMemoryResourceCheck = o.EnableNonBindingShareCoresMemoryResourceCheck
	conf.EnableNUMAAllocationReactor = o.EnableNUMAAllocationReactor
	conf.NUMABindResultResourceAllocationAnnotationKey = o.NUMABindResultResourceAllocationAnnotationKey
	conf.OOMPriorityPinnedMapAbsPath = o.OOMPriorityPinnedMapAbsPath
	conf.EnableSettingSockMem = o.EnableSettingSockMem
	conf.SetGlobalTCPMemRatio = o.SetGlobalTCPMemRatio
	conf.SetCgroupTCPMemRatio = o.SetCgroupTCPMemRatio
	conf.EnableEvictingLogCache = o.EnableEvictingLogCache
	conf.HighThreshold = o.HighThreshold
	conf.LowThreshold = o.LowThreshold
	conf.MinInterval = o.MinInterval
	conf.MaxInterval = o.MaxInterval
	conf.PathList = o.PathList
	conf.FileFilters = o.FileFilters
	conf.EnableSettingFragMem = o.EnableSettingFragMem
	conf.SetMemFragScoreAsync = o.SetMemFragScoreAsync
	return nil
}
