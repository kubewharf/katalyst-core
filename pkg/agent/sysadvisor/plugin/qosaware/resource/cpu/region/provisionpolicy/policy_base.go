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

package provisionpolicy

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

type PolicyBase struct {
	name             types.CPUProvisionPolicyName
	containerSet     map[string]sets.String
	controlKnobValue types.ControlKnob
	indicatorValue   types.Indicator
	indicatorTarget  types.Indicator

	metaCache *metacache.MetaCache
}

func NewPolicyBase(name types.CPUProvisionPolicyName, metaCache *metacache.MetaCache) *PolicyBase {
	cp := &PolicyBase{
		name:             name,
		containerSet:     make(map[string]sets.String),
		controlKnobValue: make(types.ControlKnob),
		indicatorValue:   make(types.Indicator),
		indicatorTarget:  make(types.Indicator),
		metaCache:        metaCache,
	}
	return cp
}

func (p *PolicyBase) Name() types.CPUProvisionPolicyName {
	return p.name
}

func (p *PolicyBase) SetContainerSet(containerSet map[string]sets.String) {
	p.containerSet = make(map[string]sets.String)
	for podUID, v := range containerSet {
		p.containerSet[podUID] = sets.NewString()
		for containerName := range v {
			p.containerSet[podUID].Insert(containerName)
		}
	}
}

func (p *PolicyBase) SetControlKnobValue(v types.ControlKnob) {
	p.controlKnobValue = v
}

func (p *PolicyBase) SetIndicatorValue(v types.Indicator) {
	p.indicatorValue = v
}

func (p *PolicyBase) SetIndicatorTarget(v types.Indicator) {
	p.indicatorTarget = v
}
