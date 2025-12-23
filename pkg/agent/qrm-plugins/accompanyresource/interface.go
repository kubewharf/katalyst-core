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

package accompanyresource

import (
	"fmt"
	"sync"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
)

type Plugin interface {
	AugmentTopologyHints(req *pluginapi.ResourceRequest, hints *pluginapi.ListOfTopologyHints) error
	AugmentAllocationResult(req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse) error
	ReleaseAccompanyResources(req *pluginapi.RemovePodRequest) error
}

type Registry struct {
	sync.RWMutex
	Plugins map[string]Plugin
}

func NewRegistry() *Registry {
	return &Registry{Plugins: make(map[string]Plugin)}
}

func (r *Registry) RegisterPlugin(name string, plugin Plugin) error {
	r.Lock()
	defer r.Unlock()

	_, ok := r.Plugins[name]
	if ok {
		return fmt.Errorf("associated resource plugin %v already registered", name)
	}
	r.Plugins[name] = plugin
	return nil
}

func (r *Registry) AugmentTopologyHints(req *pluginapi.ResourceRequest, hints *pluginapi.ListOfTopologyHints) (err error) {
	r.RLock()
	defer r.RUnlock()

	for name, plugin := range r.Plugins {
		if err = plugin.AugmentTopologyHints(req, hints); err != nil {
			return fmt.Errorf("accompany resource %s AugmentTopologyHints failed with error: %v", name, err)
		}
	}

	return nil
}

func (r *Registry) AugmentAllocationResult(req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse) (err error) {
	r.RLock()
	defer r.RUnlock()

	for name, plugin := range r.Plugins {
		if err = plugin.AugmentAllocationResult(req, resp); err != nil {
			return fmt.Errorf("accompany resource %s AugmentAllocationResult failed with error: %v", name, err)
		}
	}

	return nil
}

func (r *Registry) ReleaseAccompanyResources(req *pluginapi.RemovePodRequest) (err error) {
	r.RLock()
	defer r.RUnlock()

	for name, plugin := range r.Plugins {
		if err = plugin.ReleaseAccompanyResources(req); err != nil {
			return fmt.Errorf("accompany resource %s ReleaseAccompanyResources failed with error: %v", name, err)
		}
	}

	return nil
}
