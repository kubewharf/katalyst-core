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

package numabinding

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/allocation"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/numabinding/calculator"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/state"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	metricsNameNUMABindingCalculatePodCount = "numa_binding_calculate_pod_count"
)

type Manager interface {
	IsProcessing() bool
	Run(stopCh <-chan struct{})
}

type managerImpl struct {
	allocationUpdater     allocation.Updater
	numaBindingCalculator calculator.NUMABindingCalculator

	reservedCPUs machine.CPUSet

	conf       *coreconfig.Configuration
	emitter    metrics.MetricEmitter
	metaServer *metaserver.MetaServer
}

func (m *managerImpl) IsProcessing() bool {
	// TODO implement me
	return false
}

func (m *managerImpl) Run(stopCh <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go wait.JitterUntilWithContext(ctx, m.sync, 30*time.Second, 0.3, true)
	<-stopCh
}

func NewSharedNUMABindingManager(
	conf *coreconfig.Configuration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	updater allocation.Updater,
) (Manager, error) {
	reservedCPUs, reserveErr := cpuutil.GetCoresReservedForSystem(conf, metaServer, metaServer.KatalystMachineInfo, metaServer.CPUDetails.CPUs().Clone())
	if reserveErr != nil {
		return nil, fmt.Errorf("GetCoresReservedForSystem for reservedCPUsNum: %d failed with error: %v",
			conf.ReservedCPUCores, reserveErr)
	}

	numaBindingCalculator := calculator.GetOrInitNUMABindingCalculator(conf, emitter, metaServer, reservedCPUs)
	return &managerImpl{
		conf:                  conf,
		emitter:               emitter,
		metaServer:            metaServer,
		reservedCPUs:          reservedCPUs,
		allocationUpdater:     updater,
		numaBindingCalculator: calculator.WithCheckAndExecutionTimeLogging(numaBindingCalculator, emitter),
	}, nil
}

func (m *managerImpl) sync(ctx context.Context) {
	cpuState, memoryState, err := state.GetCPUMemoryReadonlyState()
	if err != nil {
		general.Errorf("get cpu/memory state failed: %v", err)
		return
	}

	numaAllocation, err := allocation.GetPodAllocations(ctx, m.metaServer, memoryState)
	if err != nil {
		general.Errorf("get numa allocation failed: %v", err)
		return
	}

	numaAllocatable, err := state.GetSharedNUMAAllocatable(
		m.metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt(),
		m.reservedCPUs,
		cpuState, memoryState)
	if err != nil {
		general.Errorf("get numa allocation failed: %v", err)
		return
	}

	general.InfoS("state summary",
		"numaAllocation", numaAllocation,
		"numaAllocatable", numaAllocatable,
		"podCount", len(numaAllocation),
		"numaCount", len(numaAllocatable))

	_ = m.emitter.StoreInt64(metricsNameNUMABindingCalculatePodCount, int64(len(numaAllocation)),
		metrics.MetricTypeNameRaw, metrics.MetricTag{
			Key: "numaCount",
			Val: strconv.Itoa(len(numaAllocatable)),
		})

	result, success, err := m.numaBindingCalculator.CalculateNUMABindingResult(numaAllocation, numaAllocatable)
	if err != nil {
		general.Errorf("calculate result failed: %v", err)
		return
	}

	if !success {
		return
	}

	err = m.allocationUpdater.UpdateAllocation(result)
	if err != nil {
		general.Errorf("update numa allocation failed: %v", err)
		return
	}
}
