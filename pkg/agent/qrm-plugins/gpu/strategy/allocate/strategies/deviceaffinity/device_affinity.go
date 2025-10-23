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

package deviceaffinity

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
)

const (
	StrategyNameDeviceAffinity = "deviceAffinity"
)

// DeviceAffinityStrategy knows how to bind devices that have affinity to each other
type DeviceAffinityStrategy struct{}

// NewDeviceAffinityStrategy creates a new device affinity strategy with the given canonical strategy
func NewDeviceAffinityStrategy() *DeviceAffinityStrategy {
	return &DeviceAffinityStrategy{}
}

var (
	_ allocate.BindingStrategy = &DeviceAffinityStrategy{}
	_ allocate.SortingStrategy = &DeviceAffinityStrategy{}
)

// Name returns the name of the binding strategy
func (s *DeviceAffinityStrategy) Name() string {
	return StrategyNameDeviceAffinity
}
