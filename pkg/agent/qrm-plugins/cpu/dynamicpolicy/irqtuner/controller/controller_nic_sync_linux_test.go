//go:build linux
// +build linux

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

package controller

import (
	"errors"
	"testing"
	"time"

	. "github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func testPeriodicTuningRateLimitsNicSyncFailures(t *testing.T) {
	t.Helper()

	tests := []struct {
		name          string
		initialized   bool
		lowThroughput bool
	}{
		{name: "startup"},
		{name: "normal throughput NICs", initialized: true},
		{name: "only low throughput NICs", initialized: true, lowThroughput: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			PatchConvey("failed discovery observes the NIC sync interval", t, func() {
				ic := &IrqTuningController{
					agentConf:       agent.NewAgentConfiguration(),
					conf:            config.NewConfiguration(),
					emitter:         metrics.DummyMetrics{},
					NicSyncInterval: NicsSyncIntervalSeconds,
				}
				nic := &machine.NicBasicInfo{
					InterfaceInfo: machine.InterfaceInfo{Name: "eth0", IfIndex: 2},
					QueueNum:      1,
				}
				manager := &NicIrqTuningManager{NicInfo: &NicInfo{NicBasicInfo: nic}}
				if tt.initialized {
					if tt.lowThroughput {
						ic.LowThroughputNics = []*NicIrqTuningManager{manager}
					} else {
						ic.Nics = []*NicIrqTuningManager{manager}
					}
					ic.LastNicSyncTime = time.Now().Add(-time.Duration(ic.NicSyncInterval) * time.Second)
				}
				oldNics, oldLowThroughputNics := ic.Nics, ic.LowThroughputNics

				// Discovery in ns1 fails after the host NIC IRQ mapping is read.
				Mock(machine.ListNetNS).Return([]machine.NetNSInfo{{}, {NSName: "ns1"}}, nil).Build()
				failDiscovery := true
				nicDiscovery := Mock(machine.ListActiveUplinkNicsFromNetNS).To(func(ns machine.NetNSInfo) ([]*machine.NicBasicInfo, error) {
					if ns.NSName == "ns1" {
						if failDiscovery {
							return nil, errors.New("netns discovery failed")
						}
						return nil, nil
					}
					return []*machine.NicBasicInfo{nic}, nil
				}).Build()
				Mock(machine.NetNSExist).Return(true).Build()
				interruptRead := Mock(machine.GetNicInterruptsInfo).Return([]machine.NicInterruptInfo{}, nil).Build()
				irqLookup := Mock(machine.GetNicQueue2Irq).Return(map[int]int{0: 32}, map[int]int{}, nil).Build()
				Mock((*NicInfo).syncIrqAffinityFromKernel).Return(nil).Build()
				tuning := Mock((*IrqTuningController).disableIrqTuning).Return().Build()
				updateManagers := Mock((*IrqTuningController).updateNicIrqTuningManagers).To(func(ic *IrqTuningController, nics []*machine.NicBasicInfo) error {
					assert.Equal(t, []*machine.NicBasicInfo{nic}, nics)
					ic.Nics = []*NicIrqTuningManager{manager}
					return nil
				}).Build()

				assert.False(t, ic.conf.EnableIrqTuning)
				start := time.Now()
				ic.periodicTuning(nil)
				assert.Equal(t, 2, nicDiscovery.Times())
				assert.Equal(t, 1, interruptRead.Times())
				assert.Equal(t, 1, irqLookup.Times())
				assert.False(t, ic.LastNicSyncTime.Before(start))
				assert.Equal(t, oldNics, ic.Nics)
				assert.Equal(t, oldLowThroughputNics, ic.LowThroughputNics)
				assert.Zero(t, tuning.Times())
				assert.Zero(t, updateManagers.Times())

				lastAttempt := ic.LastNicSyncTime
				for i := 0; i < 3; i++ {
					ic.periodicTuning(nil)
				}
				assert.Equal(t, 2, nicDiscovery.Times())
				assert.Equal(t, 1, interruptRead.Times())
				assert.Equal(t, 1, irqLookup.Times())
				assert.Equal(t, lastAttempt, ic.LastNicSyncTime)
				assert.Zero(t, updateManagers.Times())
				if tt.initialized {
					assert.Equal(t, 3, tuning.Times())
				} else {
					assert.Zero(t, tuning.Times())
				}

				// A second failure also starts a new interval.
				ic.LastNicSyncTime = time.Now().Add(-time.Duration(ic.NicSyncInterval) * time.Second)
				ic.periodicTuning(nil)
				assert.Equal(t, 4, nicDiscovery.Times())
				assert.Equal(t, 2, interruptRead.Times())
				assert.Equal(t, 2, irqLookup.Times())
				lastAttempt = ic.LastNicSyncTime
				ic.periodicTuning(nil)
				assert.Equal(t, 4, nicDiscovery.Times())
				assert.Equal(t, 2, interruptRead.Times())
				assert.Equal(t, 2, irqLookup.Times())
				assert.Equal(t, lastAttempt, ic.LastNicSyncTime)

				// Recovery is picked up at the next interval and tuning resumes.
				failDiscovery = false
				ic.LastNicSyncTime = time.Now().Add(-time.Duration(ic.NicSyncInterval) * time.Second)
				start = time.Now()
				ic.periodicTuning(nil)
				assert.Equal(t, 6, nicDiscovery.Times())
				assert.Equal(t, 3, interruptRead.Times())
				assert.Equal(t, 3, irqLookup.Times())
				assert.False(t, ic.LastNicSyncTime.Before(start))
				assert.Len(t, ic.getAllNics(), 1)
				if tt.initialized {
					assert.Zero(t, updateManagers.Times())
				} else {
					assert.Equal(t, 1, updateManagers.Times())
				}
				assert.Positive(t, tuning.Times())

				lastAttempt = ic.LastNicSyncTime
				ic.periodicTuning(nil)
				assert.Equal(t, 6, nicDiscovery.Times())
				assert.Equal(t, 3, interruptRead.Times())
				assert.Equal(t, 3, irqLookup.Times())
				assert.Equal(t, lastAttempt, ic.LastNicSyncTime)
			})
		})
	}
}
