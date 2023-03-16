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

package region

import "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"

// QoSRegionType declares pre-defined region types
type QoSRegionType string

const (
	QoSRegionTypeShare         QoSRegionType = "share"
	QoSRegionTypeIsolation     QoSRegionType = "isolation"
	QoSRegionTypeDedicated     QoSRegionType = "dedicated"      // Dedicated without numa binding
	QoSRegionTypeDedicatedNuma QoSRegionType = "dedicated-numa" // Dedicated with numa binding
)

// QoSRegion is an internal concept, managing a group of containers with similar QoS
// sensitivity and updating their resource provision and headroom by preset policies
type QoSRegion interface {
	// Name returns region's global unique identifier, combined with region type and uuid
	Name() string

	// OwnerPoolName returns the unified owner pool name of containers in region
	OwnerPoolName() string

	// Type returns region type
	Type() QoSRegionType

	// IsEmpty returns true if no container remains in region
	IsEmpty() bool

	// Clear clears all containers in region
	Clear()

	// AddContainer stores a container keyed by pod uid and container name to region
	AddContainer(podUID string, containerName string)

	// Update maximal available cpuset size for region
	SetCPULimit(value int)

	// Update reserve pool size for region
	SetReservePoolSize(value int)

	// Update reserved cpuset size for allocate for region
	SetReservedForAllocate(value int)

	// TryUpdateControlKnob runs an episode of control knob adjustment
	TryUpdateControlKnob()

	// GetControlKnobUpdated returns the latest updated control knob value
	GetControlKnobUpdated() (types.ControlKnob, error)

	// GetHeadroom returns the latest updated cpu headroom
	GetHeadroom() (int, error)
}
