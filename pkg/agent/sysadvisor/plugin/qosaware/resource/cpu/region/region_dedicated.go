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

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

type QoSRegionDedicated struct {
	*QoSRegionBase
}

func (r *QoSRegionDedicated) TryUpdateControlKnob() error {
	return nil
}

func (r *QoSRegionDedicated) GetControlKnobUpdated() (types.ControlKnob, error) {
	return nil, nil
}

func (r *QoSRegionDedicated) GetHeadroom() (resource.Quantity, error) {
	return *resource.NewQuantity(0, resource.DecimalSI), nil
}

func (r *QoSRegionDedicated) TryUpdateHeadroom() error {

	return nil
}
