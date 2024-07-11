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

package intel

import (
	"context"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/capper"
	"github.com/kubewharf/katalyst-core/pkg/util/external/power"
)

type raplCapper struct {
	raplLimiter power.PowerLimiter
}

func (r raplCapper) Cap(_ context.Context, targetWatts, currWatt int) {
	if err := r.raplLimiter.SetLimitOnBasis(targetWatts, currWatt); err != nil {
		klog.Errorf("pap: failed to power cap, current watt %d, target watt %d", currWatt, targetWatts)
	}
}

var _ capper.PowerCapper = &raplCapper{}

func NewCapper(limiter power.PowerLimiter) capper.PowerCapper {
	return &raplCapper{raplLimiter: limiter}
}
