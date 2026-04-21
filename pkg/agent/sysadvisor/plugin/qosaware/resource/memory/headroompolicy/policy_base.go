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
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
)

type PolicyBase struct {
	podSet     types.PodSet
	essentials types.ResourceEssentials

	metaReader metacache.MetaReader
	metaServer *metaserver.MetaServer

	status types.PolicyUpdateStatus
}

func NewPolicyBase(metaReader metacache.MetaReader, metaServer *metaserver.MetaServer) *PolicyBase {
	cp := &PolicyBase{
		podSet: make(types.PodSet),

		metaReader: metaReader,
		metaServer: metaServer,
		status:     types.PolicyUpdateFailed,
	}
	return cp
}

func (p *PolicyBase) SetPodSet(podSet types.PodSet) {
	p.podSet = podSet.Clone()
}

func (p *PolicyBase) SetEssentials(essentials types.ResourceEssentials) {
	p.essentials = essentials
}

func (p *PolicyBase) UpdateStatus(err error) {
	if err != nil {
		p.status = types.PolicyUpdateFailed
	} else {
		p.status = types.PolicyUpdateSucceeded
	}
}

func (p *PolicyBase) IsStatusSucceeded() bool {
	return p.status == types.PolicyUpdateSucceeded
}
