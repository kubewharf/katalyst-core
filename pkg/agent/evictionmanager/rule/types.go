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

package rule

import (
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	EvictionScopeForce = "force"
	EvictionScopeSoft  = "soft"

	EvictionScopeMemory = "memory"
	EvictionScopeCPU    = "cpu"
)

// RuledEvictPod wraps pod in a new struct to get more
// useful information to help with eviction rules.
type RuledEvictPod struct {
	*pluginapi.EvictPod
	Scope string
}

type RuledEvictPodList []*RuledEvictPod

var _ general.SourceList = RuledEvictPodList{}

func (rpList RuledEvictPodList) Len() int {
	return len(rpList)
}

func (rpList RuledEvictPodList) GetSource(index int) interface{} {
	return rpList[index]
}

func (rpList RuledEvictPodList) SetSource(index int, s interface{}) {
	rpList[index] = s.(*RuledEvictPod)
}

// getPodNames is used for testing to return the belonged pod names
func (rpList RuledEvictPodList) getPodNames() []string {
	names := []string{}
	for _, ep := range rpList {
		names = append(names, ep.Pod.Name)
	}
	return names
}
