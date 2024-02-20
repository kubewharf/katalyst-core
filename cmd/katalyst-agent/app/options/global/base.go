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

const (
	defaultKubeletReadOnlyPort      = 10255
	defaultKubeletSecurePort        = 10250
	defaultKubeletSecurePortEnabled = false
	defaultKubeletConfigURI         = "/configz"
	defaultKubeletPodsEndpoint      = "/pods"
	defaultKubeletSummaryEndpoint   = "/stats/summary"
	defaultAPIAuthTokenFile         = "/var/run/secrets/kubernetes.io/serviceaccount/token"
)

const defaultRemoteRuntimeEndpoint = "unix:///run/containerd/containerd.sock"

// BaseOptions holds all the configurations for agent-base.
// we will not try to separate this structure into several individual
// structures since it will not be used directly by other components; instead,
// we will only separate them with blanks in a single structure.
type BaseOptions struct {
	Agents             []string
	NodeName           string
	NodeAddress        string
	LockFileName       string
	LockWaitingEnabled bool

	CgroupType            string
	AdditionalCgroupPaths []string

	ReclaimRelativeRootCgroupPath string

	// configurations for kubelet
	KubeletReadOnlyPort      int
	KubeletSecurePort        int
	KubeletSecurePortEnabled bool
	KubeletConfigEndpoint    string
	KubeletPodsEndpoint      string
	KubeletSummaryEndpoint   string
	APIAuthTokenFile         string

	// configurations for runtime
	RuntimeEndpoint string

	// configurations for machine-info
	MachineNetMultipleNS   bool
	MachineNetNSDirAbsPath string
}

func NewBaseOptions() *BaseOptions {
	return &BaseOptions{
		LockFileName:       "/tmp/katalyst_agent_lock",
		LockWaitingEnabled: false,

		CgroupType: "cgroupfs",

		ReclaimRelativeRootCgroupPath: "/kubepods/besteffort",

		KubeletReadOnlyPort:      defaultKubeletReadOnlyPort,
		KubeletSecurePort:        defaultKubeletSecurePort,
		KubeletSecurePortEnabled: defaultKubeletSecurePortEnabled,
		KubeletConfigEndpoint:    defaultKubeletConfigURI,
		KubeletPodsEndpoint:      defaultKubeletPodsEndpoint,
		KubeletSummaryEndpoint:   defaultKubeletSummaryEndpoint,
		APIAuthTokenFile:         defaultAPIAuthTokenFile,

		RuntimeEndpoint: defaultRemoteRuntimeEndpoint,

		MachineNetMultipleNS: false,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *BaseOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("base")

	fs.StringSliceVar(&o.Agents, "agents", o.Agents, "The agents need to be started")
	fs.StringVar(&o.NodeName, "node-name", o.NodeName, "the name of this node")
	fs.StringVar(&o.NodeAddress, "node-address", o.NodeAddress, "the address of this node")

	fs.StringVar(&o.LockFileName, "locking-file", o.LockFileName, "The filename used as unique lock")
	fs.BoolVar(&o.LockWaitingEnabled, "locking-waiting", o.LockWaitingEnabled,
		"If failed to acquire locking files, still mark agent as healthy")

	fs.StringVar(&o.CgroupType, "cgroup-type", o.CgroupType, "The cgroup type")
	fs.StringSliceVar(&o.AdditionalCgroupPaths, "addition-cgroup-paths", o.AdditionalCgroupPaths,
		"The additional cgroup paths to be added for legacy configurations")

	fs.StringVar(&o.ReclaimRelativeRootCgroupPath, "reclaim-relative-root-cgroup-path", o.ReclaimRelativeRootCgroupPath,
		"top level cgroup path for reclaimed_cores qos level")

	fs.IntVar(&o.KubeletReadOnlyPort, "kubelet-read-only-port", o.KubeletReadOnlyPort,
		"The read-only port for the kubelet to serve")
	fs.IntVar(&o.KubeletSecurePort, "kubelet-secure-port", o.KubeletSecurePort,
		"The secure port for the kubelet to serve")
	fs.BoolVar(&o.KubeletSecurePortEnabled, "enable-kubelet-secure-port", o.KubeletSecurePortEnabled,
		"Whether to enable get contents from kubelet secure port")
	fs.StringVar(&o.KubeletConfigEndpoint, "kubelet-config-endpoint", o.KubeletConfigEndpoint,
		"The URI of kubelet config endpoint")
	fs.StringVar(&o.KubeletPodsEndpoint, "kubelet-pods-endpoint", o.KubeletPodsEndpoint,
		"The URI of kubelet pods endpoint")
	fs.StringVar(&o.KubeletSummaryEndpoint, "kubelet-summary-endpoint", o.KubeletSummaryEndpoint,
		"The URI of kubelet summary endpoint")
	fs.StringVar(&o.APIAuthTokenFile, "api-auth-token-file", o.APIAuthTokenFile,
		"The path of the API auth token file")

	fs.StringVar(&o.RuntimeEndpoint, "remote-runtime-endpoint", o.RuntimeEndpoint,
		"The endpoint of remote runtime service")

	fs.BoolVar(&o.MachineNetMultipleNS, "machine-net-multi-ns", o.MachineNetMultipleNS,
		"if set as true, we should collect network interfaces from multiple ns")
	fs.StringVar(&o.MachineNetNSDirAbsPath, "machine-net-ns-dir", o.MachineNetNSDirAbsPath,
		"if set as true, we should collect network interfaces from multiple ns")
}

// ApplyTo fills up config with options
func (o *BaseOptions) ApplyTo(c *global.BaseConfiguration) error {
	c.Agents = o.Agents
	c.NodeName = o.NodeName
	c.NodeAddress = o.NodeAddress
	c.LockFileName = o.LockFileName
	c.LockWaitingEnabled = o.LockWaitingEnabled

	c.ReclaimRelativeRootCgroupPath = o.ReclaimRelativeRootCgroupPath

	c.NetMultipleNS = o.MachineNetMultipleNS
	c.NetNSDirAbsPath = o.MachineNetNSDirAbsPath

	c.KubeletReadOnlyPort = o.KubeletReadOnlyPort
	c.KubeletSecurePortEnabled = o.KubeletSecurePortEnabled
	c.KubeletSecurePort = o.KubeletSecurePort
	c.KubeletConfigEndpoint = o.KubeletConfigEndpoint
	c.KubeletPodsEndpoint = o.KubeletPodsEndpoint
	c.KubeletSummaryEndpoint = o.KubeletSummaryEndpoint
	c.APIAuthTokenFile = o.APIAuthTokenFile

	c.RuntimeEndpoint = o.RuntimeEndpoint

	common.InitKubernetesCGroupPath(common.CgroupType(o.CgroupType), o.AdditionalCgroupPaths)
	return nil
}
