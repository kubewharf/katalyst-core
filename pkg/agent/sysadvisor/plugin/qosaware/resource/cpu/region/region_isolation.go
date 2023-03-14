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

type QoSRegionIsolation struct {
}

func (r *QoSRegionIsolation) Name() string {
	return ""
}

func (r *QoSRegionIsolation) OwnerPoolName() string {
	return ""
}

func (r *QoSRegionIsolation) Type() QoSRegionType {
	return QoSRegionTypeIsolation
}

func (r *QoSRegionIsolation) IsEmpty() bool {
	return true
}

func (r *QoSRegionIsolation) Clear() {
}

func (r *QoSRegionIsolation) AddContainer(podUID string, containerName string) {
}

func (r *QoSRegionIsolation) SetCPULimit(value int) {
}

func (r *QoSRegionIsolation) SetReservePoolSize(value int) {
}

func (r *QoSRegionIsolation) SetReservedForAllocate(value int) {
}

func (r *QoSRegionIsolation) TryUpdateControlKnob() {
}

func (r *QoSRegionIsolation) GetControlKnobUpdated() types.ControlKnob {
	return nil
}

func (r *QoSRegionIsolation) GetHeadroom() int {
	return 0
}
