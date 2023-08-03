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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// QoSRegion is internal abstraction, managing a group of containers with similar QoS sensitivity
// and updating their resource provision and headroom by preset policies
type QoSRegion interface {
	// Name returns region's global unique identifier, combined with region type and uuid
	Name() string
	// Type returns region's type
	Type() types.QoSRegionType
	// OwnerPoolName returns region's owner pool name
	OwnerPoolName() string

	// IsEmpty returns true if no container remains in region
	IsEmpty() bool
	// Clear clears all topology and container info in region
	Clear()

	// GetBindingNumas returns numa ids assigned to this region
	GetBindingNumas() machine.CPUSet
	// GetPods return the latest pod set of this region
	GetPods() types.PodSet

	// SetBindingNumas overwrites numa ids assigned to this region
	SetBindingNumas(machine.CPUSet)
	// SetEssentials updates essential region values for policy update
	SetEssentials(essentials types.ResourceEssentials)
	// AddContainer stores a container keyed by pod uid and container name to region
	AddContainer(ci *types.ContainerInfo) error

	// TryUpdateProvision runs an episode of control knob adjustment
	TryUpdateProvision()
	// TryUpdateHeadroom runs an episode of headroom estimation
	TryUpdateHeadroom()

	// GetProvision returns the latest updated control knob value
	GetProvision() (types.ControlKnob, error)
	// GetHeadroom returns the latest updated cpu headroom estimation
	GetHeadroom() (float64, error)

	// GetProvisionPolicy returns provision policy for this region,
	// the first is policy with top priority, while the second is the policy that is in-use currently
	GetProvisionPolicy() (types.CPUProvisionPolicyName, types.CPUProvisionPolicyName)
	// GetHeadRoomPolicy returns headroom policy for this region,
	// the first is policy with top priority, while the second is the policy that is in-use currently
	GetHeadRoomPolicy() (types.CPUHeadroomPolicyName, types.CPUHeadroomPolicyName)

	// GetStatus returns region status
	GetStatus() types.RegionStatus
}

// GetRegionBasicMetricTags returns metric tag slice of basic region info
func GetRegionBasicMetricTags(r QoSRegion) []metrics.MetricTag {
	ret := []metrics.MetricTag{
		{Key: "region_name", Val: r.Name()},
		{Key: "region_type", Val: string(r.Type())},
		{Key: "owner_pool_name", Val: r.OwnerPoolName()},
		{Key: "binding_numas", Val: r.GetBindingNumas().String()},
		{Key: "bound_type", Val: string(r.GetStatus().BoundType)},
	}
	return ret
}
