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
}

func NewBulkheadOptions() BulkheadOptions {
	return BulkheadOptions{
		BulkheadPrimaryRelPath:        "kubepods",
		BulkheadReclaimRelPaths:       []string{"reclaimed"},
		BulkheadReclaimNumaPrefixes:   []string{"reclaimed/reclaimed-"},
		BulkheadPartitionRelPaths:     []string{"kubepods"},
		EnableBulkheadReclaimSiblings: true,
		BulkheadWorkqueueSysfsDir:     "/sys/devices/virtual/workqueue",
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
