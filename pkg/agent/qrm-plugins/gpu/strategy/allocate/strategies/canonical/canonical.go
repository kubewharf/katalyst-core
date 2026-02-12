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

package canonical

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
)

const (
	StrategyNameCanonical = "canonical"
)

// CanonicalStrategy binds GPU devices to the allocation context
type CanonicalStrategy struct{}

// NewCanonicalStrategy creates a new default binding strategy
func NewCanonicalStrategy() *CanonicalStrategy {
	return &CanonicalStrategy{}
}

var (
	_ allocate.BindingStrategy   = &CanonicalStrategy{}
	_ allocate.FilteringStrategy = &CanonicalStrategy{}
)

// Name returns the name of the binding strategy
func (s *CanonicalStrategy) Name() string {
	return StrategyNameCanonical
}
