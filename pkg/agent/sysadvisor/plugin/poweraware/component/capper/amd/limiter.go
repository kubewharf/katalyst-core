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
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/external/power"
	utils "github.com/kubewharf/katalyst-core/pkg/util/lowlevel"
	"github.com/kubewharf/katalyst-core/pkg/util/power/amd"
)

type powerLimiter struct {
	op amd.Operation
}

func (p powerLimiter) SetLimitOnBasis(limitWatts, baseWatts int) error {
	// adjustment formula: settings = readings + limit - base
	// assuming N packages equally applied to
	reading := p.getCurrentPower()

	setting := reading + (limitWatts - baseWatts)
	klog.V(6).Infof("pap: base %d watt, target %d watt, package readings %d watt, to cap package limit %d watt",
		baseWatts, limitWatts, reading, setting)
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

func (p powerLimiter) Init() error {
	utils.PCIDevInit()
	if err := p.op.InitIOHCs(false); err != nil {
		return err
	}

	for i := 0; i < p.op.MachineInfo.SocketNum; i++ {
		p.op.MachineInfo.PowerMaxLimit[i] = p.op.GetSocketPowerMaxLimit(i)
		time.Sleep(time.Microsecond * amd.SMN_READ_INTERVAL)
	}

	return nil
}

// the default current limit = 0.9 * max limit
func (p powerLimiter) Reset() {
	for i := 0; i < p.op.MachineInfo.SocketNum; i++ {
		maxLimit := p.op.GetSocketPowerMaxLimit(i)
		klog.Infof("power max limit of socket %d: %v", i, maxLimit)
		if err := p.op.SetSocketPowerLimit(i, maxLimit*95/100); err != nil {
			klog.Errorf("reset error: %v", err)
		}
	}
}

func (p powerLimiter) getCurrentPower() int {
	var totalMicroWatts uint32
	for i := 0; i < p.op.MachineInfo.SocketNum; i++ {
		totalMicroWatts += p.op.GetSocketPower(i)
	}
	return int(totalMicroWatts / 1_000)
}

func NewPowerLimiter() power.PowerLimiter {
	op, err := amd.NewOperation()
	if err != nil {
		klog.Error("unexpected error to get amd power op object: %v", err)
		return nil
	}

	return &powerLimiter{
		op: op,
	}
}
