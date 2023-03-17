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

import (
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

type QosRegions []QoSRegion

func (rs QosRegions) Len() int           { return len(rs) }
func (rs QosRegions) Less(i, j int) bool { return rs[i].Priority() < rs[j].Priority() }
func (rs QosRegions) Swap(i, j int)      { rs[i], rs[j] = rs[j], rs[i] }

// QoSRegion is an internal concept, managing a group of containers with similar QoS
// sensitivity and updating their resource provision and headroom by preset policies
type QoSRegion interface {
	// Name returns region's global unique identifier, combined with region type and uuid
	Name() string

	// OwnerPoolName returns the unified owner pool name of containers in region
	OwnerPoolName() string

	// Type returns region type
	Type() types.QoSRegionType

	Priority() types.QosRegionPriority

	// IsEmpty returns true if no container remains in region
	IsEmpty() bool

	// Clear clears all containers in region
	Clear()

	// AddContainer stores a container keyed by pod uid and container name to region
	AddContainer(ci *types.ContainerInfo) error

	// GetNumaIDs returns numa ids this region assigned
	GetNumaIDs() sets.Int

	// GetContainerSet return containers in this region
	GetContainerSet() map[string]sets.String

	// Update maximal available cpuset size for region
	SetCPULimit(value int)

	// Update reserve pool size for region
	SetReservePoolSize(value int)

	// Update reserved cpuset size for allocate for region
	SetReservedForAllocate(value int)

	// TryUpdateControlKnob runs an episode of control knob adjustment
	TryUpdateControlKnob() error

	// TryUpdateHeadroom runs an episode of update headroom
	TryUpdateHeadroom() error

	// GetControlKnobUpdated returns the latest updated control knob value
	GetControlKnobUpdated() (types.ControlKnob, error)

	// GetHeadroom returns the latest updated cpu headroom
	GetHeadroom() (resource.Quantity, error)
}
