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

type QoSRegionDedicatedNuma struct {
}

func (r *QoSRegionDedicatedNuma) Name() string {
	return ""
}

func (r *QoSRegionDedicatedNuma) OwnerPoolName() string {
	return ""
}

func (r *QoSRegionDedicatedNuma) Type() QoSRegionType {
	return QoSRegionTypeDedicatedNuma
}

func (r *QoSRegionDedicatedNuma) IsEmpty() bool {
	return true
}

func (r *QoSRegionDedicatedNuma) Clear() {
}

func (r *QoSRegionDedicatedNuma) AddContainer(podUID string, containerName string) {
}

func (r *QoSRegionDedicatedNuma) SetCPULimit(value int) {
}

func (r *QoSRegionDedicatedNuma) SetReservePoolSize(value int) {
}

func (r *QoSRegionDedicatedNuma) SetReservedForAllocate(value int) {
}

func (r *QoSRegionDedicatedNuma) TryUpdateControlKnob() {
}

func (r *QoSRegionDedicatedNuma) GetControlKnobUpdated() types.ControlKnob {
	return nil
}

func (r *QoSRegionDedicatedNuma) GetHeadroom() int {
	return 0
}
