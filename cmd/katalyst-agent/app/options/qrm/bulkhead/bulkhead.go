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
	"strings"

	cliflag "k8s.io/component-base/cli/flag"

	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
)

type BulkheadOptions struct {
	BulkheadPrimaryRelPath        string
	BulkheadReclaimRelPaths       []string
	BulkheadReclaimNumaPrefixes   []string
	BulkheadPartitionRelPaths     []string
	EnableBulkheadReclaimSiblings bool

	BulkheadWorkqueueSysfsDir string
	BulkheadWorkqueueNames    []string

	BulkheadSystemRelPath            string
	BulkheadSystemServiceProcfsPath  string
	BulkheadSystemdCommBlacklist     []string
	BulkheadSystemKThreadCommSubstrs []string
}

func NewBulkheadOptions() BulkheadOptions {
	return BulkheadOptions{
		BulkheadPrimaryRelPath:          "kubepods",
		BulkheadReclaimRelPaths:         []string{"reclaimed"},
		BulkheadReclaimNumaPrefixes:     []string{"reclaimed/reclaimed-"},
		BulkheadPartitionRelPaths:       []string{"kubepods"},
		EnableBulkheadReclaimSiblings:   true,
		BulkheadWorkqueueSysfsDir:       "/sys/devices/virtual/workqueue",
		BulkheadSystemRelPath:           "system",
		BulkheadSystemServiceProcfsPath: "/proc",
		// Default kthread whitelist: high-load, movable kernel threads
		// whose CPU time meaningfully perturbs latency-critical userspace.
		// Do NOT include per-CPU kthreads (migration/N, ksoftirqd/N) —
		// they are not migratable.
		BulkheadSystemKThreadCommSubstrs: []string{"kswapd", "kcompactd"},
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
	fs.BoolVar(&o.EnableBulkheadReclaimSiblings, "qrm-cpu-enable-bulkhead-reclaim-siblings",
		o.EnableBulkheadReclaimSiblings, "Whether cpu bulkhead should discover reclaim sibling cgroups.")
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
}

func (o *BulkheadOptions) ApplyTo(conf *bulkheadconfig.BulkheadConfiguration) error {
	if conf == nil {
		return fmt.Errorf("nil BulkheadConfiguration")
	}
	conf.BulkheadPrimaryRelPath = normalizeRel(o.BulkheadPrimaryRelPath)
	conf.BulkheadReclaimRelPaths = normalizeRelSlice(o.BulkheadReclaimRelPaths)
	conf.BulkheadReclaimNumaPrefixes = normalizeRelSlice(o.BulkheadReclaimNumaPrefixes)
	conf.BulkheadPartitionRelPaths = normalizeRelSlice(o.BulkheadPartitionRelPaths)
	conf.EnableBulkheadReclaimSiblings = o.EnableBulkheadReclaimSiblings
	conf.BulkheadWorkqueueSysfsDir = strings.TrimSpace(o.BulkheadWorkqueueSysfsDir)
	conf.BulkheadWorkqueueNames = trimStringSlice(o.BulkheadWorkqueueNames)
	conf.BulkheadSystemRelPath = normalizeRel(o.BulkheadSystemRelPath)
	conf.BulkheadSystemServiceProcfsPath = strings.TrimSpace(o.BulkheadSystemServiceProcfsPath)
	conf.BulkheadSystemdCommBlacklist = trimStringSlice(o.BulkheadSystemdCommBlacklist)
	conf.BulkheadSystemKThreadCommSubstrs = trimStringSlice(o.BulkheadSystemKThreadCommSubstrs)
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
