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

type QoSRegionDedicated struct {
}

func (r *QoSRegionDedicated) Name() string {
	return ""
}

func (r *QoSRegionDedicated) OwnerPoolName() string {
	return ""
}

func (r *QoSRegionDedicated) Type() QoSRegionType {
	return QoSRegionTypeDedicated
}

func (r *QoSRegionDedicated) IsEmpty() bool {
	return true
}

func (r *QoSRegionDedicated) Clear() {
}

func (r *QoSRegionDedicated) AddContainer(podUID string, containerName string) {
}

func (r *QoSRegionDedicated) SetCPULimit(value int) {
}

func (r *QoSRegionDedicated) SetReservePoolSize(value int) {
}

func (r *QoSRegionDedicated) SetReservedForAllocate(value int) {
}

func (r *QoSRegionDedicated) TryUpdateControlKnob() {
}

func (r *QoSRegionDedicated) GetControlKnobUpdated() types.ControlKnob {
	return nil
}

func (r *QoSRegionDedicated) GetHeadroom() int {
	return 0
}
