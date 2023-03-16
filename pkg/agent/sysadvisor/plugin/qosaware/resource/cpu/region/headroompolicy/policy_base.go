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

package headroompolicy

import (
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

type PolicyBase struct {
	name         types.CPUHeadroomPolicyName
	containerSet map[string]sets.String

	metaCache *metacache.MetaCache
}

func NewPolicyBase(name types.CPUHeadroomPolicyName, metaCache *metacache.MetaCache) *PolicyBase {
	cp := &PolicyBase{
		name:         name,
		containerSet: make(map[string]sets.String),
		metaCache:    metaCache,
	}
	return cp
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
