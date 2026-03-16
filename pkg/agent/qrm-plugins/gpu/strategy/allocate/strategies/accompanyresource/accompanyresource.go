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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/canonical"
)

const (
	StrategyNameAccompanyResource = "AccompanyResource"
)

// AccompanyResourceStrategy allocates devices based on the allocation of an accompany resource.
type AccompanyResourceStrategy struct {
	targetDeviceName string
	canonical.CanonicalStrategy
}

func NewAccompanyResourceStrategy() *AccompanyResourceStrategy {
	return &AccompanyResourceStrategy{}
}

var _ allocate.BindingStrategy = &AccompanyResourceStrategy{}

// Name returns the name of the strategy.
func (s *AccompanyResourceStrategy) Name() string {
	return StrategyNameAccompanyResource
}
