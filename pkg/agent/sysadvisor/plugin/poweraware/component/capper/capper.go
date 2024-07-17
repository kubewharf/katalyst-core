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

package capper

import (
	"context"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/util/external/power"
)

type PowerCapper interface {
	Cap(ctx context.Context, targetWatts, currWatt int)
}

type powerCapper struct {
	limiter power.PowerLimiter
}

func (p powerCapper) Cap(_ context.Context, targetWatts, currWatt int) {
	if err := p.limiter.SetLimitOnBasis(targetWatts, currWatt); err != nil {
		klog.Errorf("pap: failed to power cap, current watt %d, target watt %d", currWatt, targetWatts)
	}
}

func NewPowerCapper(limiter power.PowerLimiter) PowerCapper {
	return &powerCapper{limiter: limiter}
}
