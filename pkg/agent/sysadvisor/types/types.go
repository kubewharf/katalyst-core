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

package types

import (
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

const (
	AdvisorPluginNameQoSAware      = "qos_aware"
	AdvisorPluginNameMetaCache     = "metacache"
	AdvisorPluginNameMetricEmitter = "metric_emitter"
)

// QoSResourceName describes different resources under qos aware control
type QoSResourceName string

const (
	QoSResourceCPU    QoSResourceName = "cpu"
	QoSResourceMemory QoSResourceName = "memory"
)

// ContainerInfo contains container information for sysadvisor plugins
type ContainerInfo struct {
	// Metadata unchanged during container's lifecycle
	PodUID         string
	PodNamespace   string
	PodName        string
	ContainerName  string
	ContainerType  v1alpha1.ContainerType
	ContainerIndex int
	Labels         map[string]string
	Annotations    map[string]string
	QoSLevel       string
	CPURequest     float64
	CPULimit       float64
	MemoryRequest  float64
	MemoryLimit    float64

	// Allocation information changing by list and watch
	RampUp                           bool
	OriginOwnerPoolName              string
	TopologyAwareAssignments         TopologyAwareAssignment
	OriginalTopologyAwareAssignments TopologyAwareAssignment

	// QoS information updated by advisor
	RegionNames   sets.String
	Isolated      bool
	OwnerPoolName string
}

// ContainerEntries stores container info keyed by container name
type ContainerEntries map[string]*ContainerInfo

// PodEntries stores container info keyed by pod uid and container name
type PodEntries map[string]ContainerEntries

// PodSet stores container names keyed by pod uid
type PodSet map[string]sets.String

// ResourceEssentials defines essential (const) variables, and those variables may be adjusted by KCC
type ResourceEssentials struct {
	EnableReclaim       bool
	ResourceUpperBound  float64
	ResourceLowerBound  float64
	ReservedForReclaim  float64
	ReservedForAllocate float64
}

// PolicyUpdateStatus works as a flag indicating update result
type PolicyUpdateStatus string

const (
	PolicyUpdateSucceeded PolicyUpdateStatus = "succeeded"
	PolicyUpdateFailed    PolicyUpdateStatus = "failed"
)

// FirstOrderPIDParams holds parameters for pid controller in rama policy
type FirstOrderPIDParams struct {
	Kpp                  float64
	Kpn                  float64
	Kdp                  float64
	Kdn                  float64
	AdjustmentUpperBound float64
	AdjustmentLowerBound float64
	DeadbandUpperPct     float64
	DeadbandLowerPct     float64
}
