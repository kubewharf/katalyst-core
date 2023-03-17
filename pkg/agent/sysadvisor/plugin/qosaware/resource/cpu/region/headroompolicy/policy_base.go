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
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
)

type PolicyBase struct {
	ContainerSet map[string]sets.String
	CPULimit     int64
	CPUReserved  int64

	MetaCache  *metacache.MetaCache
	MetaServer *metaserver.MetaServer
	Mutex      sync.RWMutex
}

func NewPolicyBase(metaCache *metacache.MetaCache, metaServer *metaserver.MetaServer) *PolicyBase {
	cp := &PolicyBase{
		ContainerSet: make(map[string]sets.String),
		MetaCache:    metaCache,
		MetaServer:   metaServer,
	}
	return cp
}

func (p *PolicyBase) SetContainerSet(containerSet map[string]sets.String) {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	p.ContainerSet = make(map[string]sets.String)
	for podUID, v := range containerSet {
		p.ContainerSet[podUID] = sets.NewString()
		for containerName := range v {
			p.ContainerSet[podUID].Insert(containerName)
		}
	}
}

func (p *PolicyBase) SetCPULimit(limit int64) {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	p.CPULimit = limit
}

func (p *PolicyBase) SetCPUReserved(reserved int64) {
	p.Mutex.Lock()
	defer p.Mutex.Unlock()
	p.CPUReserved = reserved
}
