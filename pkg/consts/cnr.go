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

package consts

// File cnr.go defines constant keys for CustomNodeResource (CNR) zone attributes.
// It centralizes the attribute names used by topology collectors and adapters so
// CNR producers and consumers stay consistent across the stack.

const (
	// ZoneAttributeNameNUMADistance is the attribute key for NUMA distance list data.
	// Example: a NUMA zone may expose "20,10" to represent distances to all NUMA nodes.
	// Note: the value is derived from sysfs distance files and serialized as a string.
	ZoneAttributeNameNUMADistance = "numa_distance"
	// ZoneAttributeNameThreadTopologyInfo is the attribute key for thread sibling mapping.
	// Example: "0:3,1:2,2:1,3:0" maps each CPU to its hyper-thread sibling.
	// Note: this attribute is reported only when thread-topology reporting is enabled.
	ZoneAttributeNameThreadTopologyInfo = "thread_topology_info"
	// ZoneAttributeNameReservedCPUList is the attribute key for reserved CPUs in a NUMA zone.
	// Example: "0-1,8-9" indicates CPUs reserved for system or agent usage on that NUMA node.
	// Note: the value is a cpuset string and is emitted only when the set is non-empty.
	ZoneAttributeNameReservedCPUList = "reserved_cpu_list"
	// ZoneAttributeNameCPULists is the attribute key for CPU lists in a cache group zone.
	// Example: a cache group zone may expose "2-5,10-13" to show its CPU membership.
	// Note: this is typically reported for L3 cache groups and may be vendor-gated.
	ZoneAttributeNameCPULists = "cpu_lists"
)
