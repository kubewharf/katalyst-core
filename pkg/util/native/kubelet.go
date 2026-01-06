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

package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

const (
	defaultTimeout = time.Second * 10
)

// MemoryReservation specifies the memory reservation of different types for each NUMA node
type MemoryReservation struct {
	NumaNode int32
	Limits   v1.ResourceList
}

// KubeletConfiguration contains the configuration for the Kubelet
// This struct is a simplification of the definition in the kubelet api repo, holding only the fields used by katalyst.
type KubeletConfiguration struct {
	// cpuManagerPolicy is the name of the policy to use.
	// Requires the CPUManager feature gate to be enabled.
	// Default: "None"
	// +optional
	CPUManagerPolicy string `json:"cpuManagerPolicy,omitempty"`
	// memoryManagerPolicy is the name of the policy to use by memory manager.
	// Requires the MemoryManager feature gate to be enabled.
	// Default: "none"
	// +optional
	MemoryManagerPolicy string `json:"memoryManagerPolicy,omitempty"`
	// topologyManagerPolicy is the name of the topology manager policy to use.
	// Valid values include:
	//
	// - `restricted`: kubelet only allows pods with optimal NUMA node alignment for
	//   requested resources;
	// - `best-effort`: kubelet will favor pods with NUMA alignment of CPU and device
	//   resources;
	// - `none`: kubelet has no knowledge of NUMA alignment of a pod's CPU and device resources.
	// - `single-numa-node`: kubelet only allows pods with a single NUMA alignment
	//   of CPU and device resources.
	//
	// Policies other than "none" require the TopologyManager feature gate to be enabled.
	// Default: "none"
	// +optional
	TopologyManagerPolicy string `json:"topologyManagerPolicy,omitempty"`
	// topologyManagerScope represents the scope of topology hint generation
	// that topology manager requests and hint providers generate. Valid values include:
	//
	// - `container`: topology policy is applied on a per-container basis.
	// - `pod`: topology policy is applied on a per-pod basis.
	//
	// "pod" scope requires the TopologyManager feature gate to be enabled.
	// Default: "container"
	// +optional
	TopologyManagerScope string `json:"topologyManagerScope,omitempty"`
	// featureGates is a map of feature names to bools that enable or disable experimental
	// features. This field modifies piecemeal the built-in default values from
	// "k8s.io/kubernetes/pkg/features/kube_features.go".
	// Default: nil
	// +optional
	FeatureGates map[string]bool `json:"featureGates,omitempty"`

	/* the following fields are meant for Node Allocatable */

	// systemReserved is a set of ResourceName=ResourceQuantity (e.g. cpu=200m,memory=150G)
	// pairs that describe resources reserved for non-kubernetes components.
	// Currently only cpu and memory are supported.
	// See http://kubernetes.io/docs/user-guide/compute-resources for more detail.
	// Default: nil
	// +optional
	SystemReserved map[string]string `json:"systemReserved,omitempty"`
	// kubeReserved is a set of ResourceName=ResourceQuantity (e.g. cpu=200m,memory=150G) pairs
	// that describe resources reserved for kubernetes system components.
	// Currently, cpu, memory and local storage for root file system are supported.
	// See https://kubernetes.io/docs/concepts/configuration/manage-resources-containers/
	// for more details.
	// Default: nil
	// +optional
	KubeReserved map[string]string `json:"kubeReserved,omitempty"`
	// The reservedSystemCPUs option specifies the CPU list reserved for the host
	// level system threads and kubernetes related threads. This provide a "static"
	// CPU list rather than the "dynamic" list by systemReserved and kubeReserved.
	// This option does not support systemReservedCgroup or kubeReservedCgroup.
	ReservedSystemCPUs string `json:"reservedSystemCPUs,omitempty"`
	// ReservedMemory specifies a comma-separated list of memory reservations for NUMA nodes.
	// The parameter makes sense only in the context of the memory manager feature. The memory manager will not allocate reserved memory for container workloads.
	// For example, if you have a NUMA0 with 10Gi of memory and the ReservedMemory was specified to reserve 1Gi of memory at NUMA0,
	// the memory manager will assume that only 9Gi is available for allocation.
	// You can specify a different amount of NUMA node and memory types.
	// You can omit this parameter at all, but you should be aware that the amount of reserved memory from all NUMA nodes
	// should be equal to the amount of memory specified by the node allocatable features(https://kubernetes.io/docs/tasks/administer-cluster/reserve-compute-resources/#node-allocatable).
	// If at least one node allocatable parameter has a non-zero value, you will need to specify at least one NUMA node.
	// Also, avoid specifying:
	// 1. Duplicates, the same NUMA node, and memory type, but with a different value.
	// 2. zero limits for any memory type.
	// 3. NUMAs nodes IDs that do not exist under the machine.
	// 4. memory types except for memory and hugepages-<size>
	ReservedMemory []MemoryReservation `json:"reservedMemory,omitempty"`

	/* the following fields are introduced for compatibility with KubeWharf Kubernetes distro */

	// NumericTopologyAlignResources is a list of resources which need to be aligned numa affinity
	// in numeric topology policy.
	// Default: [cpu, memory]
	// +optional
	NumericTopologyAlignResources []string `json:"numericTopologyAlignResources,omitempty"`
}

// GetAndUnmarshalForHttps gets data from the given url and unmarshal it into the given struct.
func GetAndUnmarshalForHttps(ctx context.Context, port int, nodeAddress, endpoint, authTokenFile string, v interface{}) error {
	uri, err := generateURI(port, nodeAddress, endpoint)
	if err != nil {
		return err
	}
	restConfig, err := insecureConfig(uri, authTokenFile)
	if err != nil {
		return fmt.Errorf("failed to initialize rest config for kubelet config uri: %w", err)
	}

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(restConfig)
	if err != nil {
		return err
	}

	bytes, err := discoveryClient.RESTClient().
		Get().
		Timeout(defaultTimeout).
		Do(ctx).
		Raw()
	if err != nil {
		return err
	}

	if err = json.Unmarshal(bytes, v); err != nil {
		return fmt.Errorf("failed to unmarshal json for kubelet config: %w", err)
	}

	return nil
}

func generateURI(port int, nodeAddress, endpoint string) (string, error) {
	if nodeAddress == "" {
		return "", fmt.Errorf("node address is empty")
	}
	u, err := url.ParseRequestURI(fmt.Sprintf("https://%s%s", net.JoinHostPort(nodeAddress, strconv.Itoa(port)), endpoint))
	if err != nil {
		return "", fmt.Errorf("failed to parse -kubelet-config-uri: %w", err)
	}

	return u.String(), nil
}

func insecureConfig(host, tokenFile string) (*rest.Config, error) {
	if tokenFile == "" {
		return nil, fmt.Errorf("api auth token file must be defined")
	}
	if len(host) == 0 {
		return nil, fmt.Errorf("kubelet host must be defined")
	}

	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}

	tlsClientConfig := rest.TLSClientConfig{Insecure: true}

	return &rest.Config{
		Host:            host,
		TLSClientConfig: tlsClientConfig,
		BearerToken:     string(token),
		BearerTokenFile: tokenFile,
	}, nil
}
