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

package amd

import (
	"context"
	"sync"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/component/capper"
	"github.com/kubewharf/katalyst-core/pkg/util/external/power"
	utils "github.com/kubewharf/katalyst-core/pkg/util/lowlevel"
	"github.com/kubewharf/katalyst-core/pkg/util/power/amd"
)

var (
	once       sync.Once
	amdPowerOp *powerCapLimiter
)

type powerCapLimiter struct {
	op amd.Operation
}

func (p powerCapLimiter) Cap(ctx context.Context, targetWatts, currWatt int) {
	if err := p.SetLimitOnBasis(targetWatts, currWatt); err != nil {
		klog.Errorf("pap: failed to power cap, current watt %d, target watt %d", currWatt, targetWatts)
	}
}

func (p powerCapLimiter) getCurrentPower() int {
	var totalMicroWatts uint32
	for i := 0; i < p.op.MachineInfo.SocketNum; i++ {
		totalMicroWatts += p.op.GetSocketPower(i)
	}
	return int(totalMicroWatts / 1_000)
}

func (p powerCapLimiter) SetLimitOnBasis(limitWatts, baseWatts int) error {
	// adjustment formula: settings = readings + limit - base
	// assuming N packages equally applied to
	reading := p.getCurrentPower()

	setting := reading + (limitWatts - baseWatts)
	if p.op.MachineInfo.SocketNum == 0 {
		return errors.New("should have at lease 1 physical socket")
	}

	targetMicroWattsPerSocket := setting / p.op.MachineInfo.SocketNum * 1_000

	for i := 0; i < p.op.MachineInfo.SocketNum; i++ {
		err := p.op.SetSocketPowerLimit(i, uint32(targetMicroWattsPerSocket))
		if err != nil {
			return errors.Wrap(err, "amd set socket power fail")
		}
	}

	return nil
}

// Reset is method of power capper, which sets 0 in amd case
func (p powerCapLimiter) Reset() {
	for i := 0; i < p.op.MachineInfo.SocketNum; i++ {
		p.op.SetSocketPowerLimit(i, 0)
	}
}

func (p powerCapLimiter) Init() error {
	utils.PCIDevInit()
	return p.op.InitIOHCs(false)
}

func getSingleton() *powerCapLimiter {
	once.Do(func() {
		if op, err := amd.NewOperation(); err == nil {
			amdPowerOp = &powerCapLimiter{
				op: op,
			}
		}
	})
	return amdPowerOp
}

func NewAMDPowerLimiter() power.PowerLimiter {
	return getSingleton()
}

func NewAMDPowerCapper() capper.PowerCapper {
	return getSingleton()
}
