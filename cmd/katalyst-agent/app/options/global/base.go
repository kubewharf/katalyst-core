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
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
	cliflag "k8s.io/component-base/cli/flag"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
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
	GeneralRelativeCgroupPaths    []string
	OptionalRelativeCgroupPaths   []string

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
	MachineNetMultipleNS                                bool
	MachineNetNSDirAbsPath                              string
	MachineNetAllocatableNS                             []string
	MachineSiblingNumaMaxDistance                       int
	MachineSiblingNumaMemoryBandwidthCapacity           resource.QuantityValue
	MachineSiblingNumaMemoryBandwidthAllocatableRate    float64
	MachineSiblingNumaMemoryBandwidthAllocatableRateMap map[string]string
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

		MachineNetMultipleNS:                             false,
		MachineNetAllocatableNS:                          []string{"*"},
		MachineSiblingNumaMemoryBandwidthAllocatableRate: 1.0,
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
	fs.StringSliceVar(&o.GeneralRelativeCgroupPaths, "malachite-general-relative-cgroup-paths", o.GeneralRelativeCgroupPaths,
		"The cgroup paths of standalone services which not managed by kubernetes, errors will occur if these paths not existed")
	fs.StringSliceVar(&o.OptionalRelativeCgroupPaths, "malachite-optional-relative-cgroup-paths", o.OptionalRelativeCgroupPaths,
		"The cgroup paths of standalone services which not managed by kubernetes")

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
		"the absolute path of network ns dir")
	fs.StringSliceVar(&o.MachineNetAllocatableNS, "machine-net-allocatable-ns", o.MachineNetAllocatableNS,
		"the list of allocatable network namespaces, '*' means all namespaces are allocatable, "+
			"'ns2' means ns2 is allocatable, '-ns3' not ns3 is not allocatable")

	fs.IntVar(&o.MachineSiblingNumaMaxDistance, "machine-sibling-numa-max-distance", o.MachineSiblingNumaMaxDistance,
		"The maximum distance between sibling NUMA nodes. If not set, the maximum distance defaults to the distance to itself.")
	fs.Var(&o.MachineSiblingNumaMemoryBandwidthCapacity, "machine-sibling-numa-memory-bandwidth-capacity",
		"if set the sibling numa memory bandwidth capacity, the per memory bandwidth capacity and allocatable will be reported to numa zone of cnr")
	fs.Float64Var(&o.MachineSiblingNumaMemoryBandwidthAllocatableRate, "machine-sibling-numa-memory-bandwidth-allocatable-rate", o.MachineSiblingNumaMemoryBandwidthAllocatableRate,
		"the rate between sibling numa memory bandwidth allocatable to its capacity")
	fs.StringToStringVar(&o.MachineSiblingNumaMemoryBandwidthAllocatableRateMap, "machine-sibling-numa-memory-bandwidth-allocatable-rate-map", o.MachineSiblingNumaMemoryBandwidthAllocatableRateMap,
		"the rate map from cpu codename to sibling numa memory bandwidth allocatable rate")
}

// ApplyTo fills up config with options
func (o *BaseOptions) ApplyTo(c *global.BaseConfiguration) error {
	c.Agents = o.Agents
	c.NodeName = o.NodeName
	c.NodeAddress = o.NodeAddress
	c.LockFileName = o.LockFileName
	c.LockWaitingEnabled = o.LockWaitingEnabled

	c.ReclaimRelativeRootCgroupPath = o.ReclaimRelativeRootCgroupPath
	c.GeneralRelativeCgroupPaths = o.GeneralRelativeCgroupPaths
	c.OptionalRelativeCgroupPaths = o.OptionalRelativeCgroupPaths
	c.CgroupType = o.CgroupType
	c.AdditionalK8sCgroupPaths = o.AdditionalCgroupPaths

	c.NetMultipleNS = o.MachineNetMultipleNS
	c.NetNSDirAbsPath = o.MachineNetNSDirAbsPath
	c.NetAllocatableNS = o.MachineNetAllocatableNS
	c.SiblingNumaMaxDistance = o.MachineSiblingNumaMaxDistance
	c.SiblingNumaMemoryBandwidthCapacity = o.MachineSiblingNumaMemoryBandwidthCapacity.Quantity.Value()
	c.SiblingNumaMemoryBandwidthAllocatableRate = o.MachineSiblingNumaMemoryBandwidthAllocatableRate
	c.SiblingNumaMemoryBandwidthAllocatableRateMap = mapStrToFloat64(o.MachineSiblingNumaMemoryBandwidthAllocatableRateMap)

	c.KubeletReadOnlyPort = o.KubeletReadOnlyPort
	c.KubeletSecurePortEnabled = o.KubeletSecurePortEnabled
	c.KubeletSecurePort = o.KubeletSecurePort
	c.KubeletConfigEndpoint = o.KubeletConfigEndpoint
	c.KubeletPodsEndpoint = o.KubeletPodsEndpoint
	c.KubeletSummaryEndpoint = o.KubeletSummaryEndpoint
	c.APIAuthTokenFile = o.APIAuthTokenFile

	c.RuntimeEndpoint = o.RuntimeEndpoint
	return nil
}

func mapStrToFloat64(input map[string]string) map[string]float64 {
	output := make(map[string]float64)
	for cpuCodeName, allocatableRateStr := range input {
		if cpuCodeName == "" || allocatableRateStr == "" {
			continue
		}

		allocatableRate, err := strconv.ParseFloat(allocatableRateStr, 64)
		if err != nil {
			klog.Warningf("Invalid float value for key %q (%q), skipped: %v", cpuCodeName, allocatableRateStr, err)
			continue
		}

		output[cpuCodeName] = allocatableRate
	}
	return output
}
