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

package global

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

type BaseOptions struct {
	Agents             []string
	NodeName           string
	NodeAddress        string
	LockFileName       string
	LockWaitingEnabled bool

	CgroupType            string
	AdditionalCgroupPaths []string

	MachineNetMultipleNS   bool
	MachineNetNSDirAbsPath string
}

func NewBaseOptions() *BaseOptions {
	return &BaseOptions{
		MachineNetMultipleNS: false,

		LockFileName:       "/tmp/katalyst_agent_lock",
		LockWaitingEnabled: false,

		CgroupType: "cgroupfs",
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *BaseOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("base")

	fs.StringSliceVar(&o.Agents, "agents", o.Agents, "The agents need to be started")
	fs.StringVar(&o.NodeName, "node-name", o.NodeName, "the name of this node")
	fs.StringVar(&o.NodeAddress, "node-address", o.NodeAddress, "the address of this node")

	fs.BoolVar(&o.MachineNetMultipleNS, "machine-net-multi-ns", o.MachineNetMultipleNS,
		"if set as true, we should collect network interfaces from multiple ns")
	fs.StringVar(&o.MachineNetNSDirAbsPath, "machine-net-ns-dir", o.MachineNetNSDirAbsPath,
		"if set as true, we should collect network interfaces from multiple ns")

	fs.StringVar(&o.LockFileName, "locking-file", o.LockFileName, "The filename used as unique lock")
	fs.BoolVar(&o.LockWaitingEnabled, "locking-waiting", o.LockWaitingEnabled,
		"If failed to acquire locking files, still mark agent as healthy")

	fs.StringVar(&o.CgroupType, "cgroup-type", o.CgroupType, "The cgroup type")
	fs.StringSliceVar(&o.AdditionalCgroupPaths, "addition-cgroup-paths", o.AdditionalCgroupPaths,
		"The additional cgroup paths to be added for legacy configurations")
}

// ApplyTo fills up config with options
func (o *BaseOptions) ApplyTo(c *global.BaseConfiguration) error {
	c.Agents = o.Agents
	c.NodeName = o.NodeName
	c.NodeAddress = o.NodeAddress
	c.LockFileName = o.LockFileName
	c.LockWaitingEnabled = o.LockWaitingEnabled

	c.NetMultipleNS = o.MachineNetMultipleNS
	c.NetNSDirAbsPath = o.MachineNetNSDirAbsPath

	common.InitKubernetesCGroupPath(common.CgroupType(o.CgroupType), o.AdditionalCgroupPaths)
	return nil
}
