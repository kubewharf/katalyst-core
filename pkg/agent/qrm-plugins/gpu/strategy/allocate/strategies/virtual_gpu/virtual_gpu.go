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

package virtual_gpu

import "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"

const (
	StrategyNameVirtualGPU = "virtual-gpu"
)

// VirtualGPUStrategy filters GPU devices based on available GPU compute
type VirtualGPUStrategy struct{}

var (
	_ allocate.FilteringStrategy = &VirtualGPUStrategy{}
	_ allocate.SortingStrategy   = &VirtualGPUStrategy{}
)

// NewVirtualGPUStrategy creates a new GPU compute filtering strategy
func NewVirtualGPUStrategy() *VirtualGPUStrategy {
	return &VirtualGPUStrategy{}
}

// Name returns the name of the filtering strategy
func (s *VirtualGPUStrategy) Name() string {
	return StrategyNameVirtualGPU
}
