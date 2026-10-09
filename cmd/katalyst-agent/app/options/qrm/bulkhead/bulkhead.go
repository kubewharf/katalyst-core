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

package bulkhead

import (
	"fmt"
	"math"
	"strings"
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
)

type BulkheadOptions struct {
	BulkheadPrimaryRelPath         string
	BulkheadReclaimRelPaths        []string
	BulkheadReclaimNumaPrefixes    []string
	BulkheadPartitionRelPaths      []string
	BulkheadReclaimSiblingRelPaths []string
	EnableBulkheadReclaimSiblings  bool

	EnableBulkheadCpusetTopologyOnCgroupV2    bool
	PreserveReclaimCPUSetWhenTopologyDisabled bool

	BulkheadWorkqueueSysfsDir string
	BulkheadWorkqueueNames    []string

	BulkheadSystemRelPath            string
	BulkheadSystemServiceProcfsPath  string
	BulkheadSystemdCommBlacklist     []string
	BulkheadSystemKThreadCommSubstrs []string

	MaxCPUsDrainRatio           float64
	TopologyConvergenceDeadline time.Duration
	MaxDeadlockProbeOperations  int
	EnableAdmissionLeafDefer    bool
	AdmissionMaxRequiredWrites  int
	AdmissionSafeDuration       time.Duration
}

func NewBulkheadOptions() BulkheadOptions {
	return BulkheadOptions{
		BulkheadPrimaryRelPath:          "kubepods",
		BulkheadReclaimRelPaths:         []string{"reclaimed"},
		BulkheadReclaimNumaPrefixes:     []string{"reclaimed/reclaimed-"},
		BulkheadReclaimSiblingRelPaths:  []string{"system"},
		EnableBulkheadReclaimSiblings:   true,
		BulkheadWorkqueueSysfsDir:       "/sys/devices/virtual/workqueue",
		BulkheadSystemRelPath:           "system",
		BulkheadSystemServiceProcfsPath: "/proc",
		// Default kthread whitelist: high-load, movable kernel threads
		// whose CPU time meaningfully perturbs latency-critical userspace.
		// Do NOT include per-CPU kthreads (migration/N, ksoftirqd/N) —
		// they are not migratable.
		BulkheadSystemKThreadCommSubstrs: []string{"kswapd", "kcompactd"},
		TopologyConvergenceDeadline:      bulkheadconfig.DefaultTopologyConvergenceDeadline,
		MaxDeadlockProbeOperations:       0,
		EnableAdmissionLeafDefer:         true,
		AdmissionMaxRequiredWrites:       0,
		AdmissionSafeDuration:            5 * time.Second,
	}
}

func (o *BulkheadOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("cpu_resource_plugin")
	fs.StringVar(&o.BulkheadPrimaryRelPath, "qrm-cpu-bulkhead-primary-rel-path",
		o.BulkheadPrimaryRelPath, "The primary cgroup relative path managed by cpu bulkhead.")
	fs.StringSliceVar(&o.BulkheadReclaimRelPaths, "qrm-cpu-bulkhead-reclaim-rel-paths",
		o.BulkheadReclaimRelPaths, "The reclaim cgroup relative paths managed by cpu bulkhead.")
	fs.StringSliceVar(&o.BulkheadReclaimNumaPrefixes, "qrm-cpu-bulkhead-reclaim-numa-prefixes",
		o.BulkheadReclaimNumaPrefixes, "The reclaim per-NUMA cgroup relative path prefixes managed by cpu bulkhead.")
	fs.StringSliceVar(&o.BulkheadPartitionRelPaths, "qrm-cpu-bulkhead-partition-rel-paths",
		o.BulkheadPartitionRelPaths, "The cgroup relative paths whose cpuset partition should be root.")
	fs.StringSliceVar(&o.BulkheadReclaimSiblingRelPaths, "qrm-cpu-bulkhead-reclaim-sibling-rel-paths",
		o.BulkheadReclaimSiblingRelPaths, "The reclaim sibling cgroup relative paths explicitly managed by cpu bulkhead.")
	fs.BoolVar(&o.EnableBulkheadReclaimSiblings, "qrm-cpu-enable-bulkhead-reclaim-siblings",
		o.EnableBulkheadReclaimSiblings, "Whether cpu bulkhead should discover reclaim sibling cgroups.")
	fs.BoolVar(&o.EnableBulkheadCpusetTopologyOnCgroupV2, "qrm-cpu-enable-bulkhead-cpuset-topology-on-cgroupv2",
		o.EnableBulkheadCpusetTopologyOnCgroupV2, "Whether the cpu bulkhead cpuset_topology plugin runs on cgroup v2 hosts; when false the plugin is inert on cgroup v2 and only runs on cgroup v1.")
	fs.BoolVar(&o.PreserveReclaimCPUSetWhenTopologyDisabled, "qrm-cpu-bulkhead-preserve-reclaim-cpuset-when-topology-disabled",
		o.PreserveReclaimCPUSetWhenTopologyDisabled, "Whether existing reclaim cgroups continue following the desired reclaim CPUSet while full topology management is disabled.")
	fs.StringVar(&o.BulkheadWorkqueueSysfsDir, "qrm-cpu-bulkhead-workqueue-sysfs-dir",
		o.BulkheadWorkqueueSysfsDir, "The workqueue sysfs directory for cpu bulkhead.")
	fs.StringSliceVar(&o.BulkheadWorkqueueNames, "qrm-cpu-bulkhead-workqueue-names",
		o.BulkheadWorkqueueNames, "The per-workqueue names whose cpumask should be adjusted by cpu bulkhead.")
	fs.StringVar(&o.BulkheadSystemRelPath, "qrm-cpu-bulkhead-system-rel-path",
		o.BulkheadSystemRelPath, "The target cgroup relative path that cpu bulkhead system_service migrates matching processes into.")
	fs.StringVar(&o.BulkheadSystemServiceProcfsPath, "qrm-cpu-bulkhead-system-service-procfs-path",
		o.BulkheadSystemServiceProcfsPath, "The procfs mount root used by cpu bulkhead system_service.")
	fs.StringSliceVar(&o.BulkheadSystemdCommBlacklist, "qrm-cpu-bulkhead-systemd-comm-blacklist",
		o.BulkheadSystemdCommBlacklist, "The userspace process comm exact-match blacklist kept in the cgroup root (not migrated) by cpu bulkhead system_service.")
	fs.StringSliceVar(&o.BulkheadSystemKThreadCommSubstrs, "qrm-cpu-bulkhead-system-kthread-comm-substrs",
		o.BulkheadSystemKThreadCommSubstrs, "The kernel-thread comm substring whitelist migrated by cpu bulkhead system_service.")
	fs.Float64Var(&o.MaxCPUsDrainRatio, "qrm-cpu-bulkhead-max-cpus-drain-ratio",
		o.MaxCPUsDrainRatio, "The maximum ratio of logical CPUs drained in one cpu bulkhead round; 0 disables the limit.")
	fs.DurationVar(&o.TopologyConvergenceDeadline, "qrm-cpu-bulkhead-topology-convergence-deadline",
		o.TopologyConvergenceDeadline, "The deadline for one cpu bulkhead topology convergence invocation.")
	fs.IntVar(&o.MaxDeadlockProbeOperations, "qrm-cpu-bulkhead-deadlock-probe-operations",
		o.MaxDeadlockProbeOperations, "The maximum projected operations used by one cpu bulkhead deadlock probe; 0 means auto-sized by topology shape.")
	fs.BoolVar(&o.EnableAdmissionLeafDefer, "qrm-cpu-bulkhead-admission-leaf-defer",
		o.EnableAdmissionLeafDefer, "Allow admission to return after parent-safe convergence and defer exact leaf convergence.")
	fs.IntVar(&o.AdmissionMaxRequiredWrites, "qrm-cpu-bulkhead-admission-max-required-writes",
		o.AdmissionMaxRequiredWrites, "Maximum required cgroup writes in one parent-safe admission; 0 disables the fixed write cap and relies on the admission duration deadline.")
	fs.DurationVar(&o.AdmissionSafeDuration, "qrm-cpu-bulkhead-admission-safe-duration",
		o.AdmissionSafeDuration, "Maximum duration spent proving parent-safe admission.")
}

func (o *BulkheadOptions) ApplyTo(conf *bulkheadconfig.BulkheadConfiguration) error {
	if conf == nil {
		return fmt.Errorf("nil BulkheadConfiguration")
	}
	if math.IsNaN(o.MaxCPUsDrainRatio) || o.MaxCPUsDrainRatio < 0 || o.MaxCPUsDrainRatio > 1 {
		return fmt.Errorf("qrm-cpu-bulkhead-max-cpus-drain-ratio must be within [0,1], got %v", o.MaxCPUsDrainRatio)
	}
	if o.TopologyConvergenceDeadline <= 0 {
		return fmt.Errorf("qrm-cpu-bulkhead-topology-convergence-deadline must be positive, got %s", o.TopologyConvergenceDeadline)
	}
	if o.MaxDeadlockProbeOperations < 0 {
		return fmt.Errorf("qrm-cpu-bulkhead-deadlock-probe-operations must be non-negative, got %d", o.MaxDeadlockProbeOperations)
	}
	if o.AdmissionMaxRequiredWrites < 0 {
		return fmt.Errorf("qrm-cpu-bulkhead-admission-max-required-writes must be non-negative, got %d", o.AdmissionMaxRequiredWrites)
	}
	if o.AdmissionSafeDuration <= 0 {
		return fmt.Errorf("qrm-cpu-bulkhead-admission-safe-duration must be positive, got %s", o.AdmissionSafeDuration)
	}
	conf.BulkheadPrimaryRelPath = normalizeRel(o.BulkheadPrimaryRelPath)
	conf.BulkheadReclaimRelPaths = normalizeRelSlice(o.BulkheadReclaimRelPaths)
	conf.BulkheadReclaimNumaPrefixes = normalizeRelSlice(o.BulkheadReclaimNumaPrefixes)
	conf.BulkheadPartitionRelPaths = normalizeRelSlice(o.BulkheadPartitionRelPaths)
	conf.BulkheadReclaimSiblingRelPaths = normalizeRelSlice(o.BulkheadReclaimSiblingRelPaths)
	conf.EnableBulkheadReclaimSiblings = o.EnableBulkheadReclaimSiblings
	conf.EnableBulkheadCpusetTopologyOnCgroupV2 = o.EnableBulkheadCpusetTopologyOnCgroupV2
	conf.PreserveReclaimCPUSetWhenTopologyDisabled = o.PreserveReclaimCPUSetWhenTopologyDisabled
	conf.BulkheadWorkqueueSysfsDir = strings.TrimSpace(o.BulkheadWorkqueueSysfsDir)
	conf.BulkheadWorkqueueNames = trimStringSlice(o.BulkheadWorkqueueNames)
	conf.BulkheadSystemRelPath = normalizeRel(o.BulkheadSystemRelPath)
	conf.BulkheadSystemServiceProcfsPath = strings.TrimSpace(o.BulkheadSystemServiceProcfsPath)
	conf.BulkheadSystemdCommBlacklist = trimStringSlice(o.BulkheadSystemdCommBlacklist)
	conf.BulkheadSystemKThreadCommSubstrs = trimStringSlice(o.BulkheadSystemKThreadCommSubstrs)
	conf.TopologyDrainSelection.MaxCPUsDrainRatio = o.MaxCPUsDrainRatio
	conf.TopologyConvergenceBudget.DeadlineDuration = o.TopologyConvergenceDeadline
	conf.TopologyConvergenceBudget.MaxDeadlockProbeOperations = o.MaxDeadlockProbeOperations
	conf.EnableAdmissionLeafDefer = o.EnableAdmissionLeafDefer
	conf.AdmissionMaxRequiredWrites = o.AdmissionMaxRequiredWrites
	conf.AdmissionSafeDuration = o.AdmissionSafeDuration
	if len(conf.BulkheadReclaimNumaPrefixes) > len(conf.BulkheadReclaimRelPaths) {
		return fmt.Errorf("qrm cpu bulkhead reclaim numa prefixes count %d exceeds reclaim rel paths count %d",
			len(conf.BulkheadReclaimNumaPrefixes), len(conf.BulkheadReclaimRelPaths))
	}
	return nil
}

func normalizeRel(rel string) string {
	return strings.Trim(strings.TrimSpace(rel), "/")
}

func normalizeRelSlice(in []string) []string {
	out := make([]string, 0, len(in))
	for _, rel := range in {
		if normalized := normalizeRel(rel); normalized != "" {
			out = append(out, normalized)
		}
	}
	return out
}

func trimStringSlice(in []string) []string {
	out := make([]string, 0, len(in))
	for _, value := range in {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
