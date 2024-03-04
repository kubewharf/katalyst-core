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

package plugin

import (
	"math"
	"strconv"

	"go.uber.org/atomic"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/helper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	MemoryGuard = "memory-guard"

	reconcileStatusSucceeded = "succeeded"
	reconcileStatusFailed    = "failed"
)

type memoryGuard struct {
	metaReader                    metacache.MetaReader
	metaServer                    *metaserver.MetaServer
	emitter                       metrics.MetricEmitter
	reclaimRelativeRootCgroupPath string
	reclaimMemoryLimit            *atomic.Int64
	reconcileStatus               *atomic.String
	minCriticalWatermark          int64
	conf                          *config.Configuration
}

func NewMemoryGuard(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	return &memoryGuard{
		metaReader:                    metaReader,
		metaServer:                    metaServer,
		emitter:                       emitter,
		reclaimRelativeRootCgroupPath: conf.ReclaimRelativeRootCgroupPath,
		reclaimMemoryLimit:            atomic.NewInt64(-1),
		reconcileStatus:               atomic.NewString(reconcileStatusFailed),
		minCriticalWatermark:          conf.MinCriticalWatermark,
		conf:                          conf,
	}
}

func (mg *memoryGuard) Reconcile(status *types.MemoryPressureStatus) error {
	mg.reconcileStatus.Store(reconcileStatusFailed)
	reclaimMemoryLimit := .0
	availNUMAs, _, err := helper.GetAvailableNUMAsAndReclaimedCores(mg.conf, mg.metaReader, mg.metaServer)
	if err != nil {
		return err
	}

	watermarkScaleFactor, err := mg.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		general.ErrorS(err, "Can not get system watermark scale factor")
		return err
	}

	for _, numaID := range availNUMAs.ToSliceInt() {
		reclaimedCoresUsed, err := mg.metaServer.GetCgroupNumaMetric(mg.reclaimRelativeRootCgroupPath, numaID, consts.MetricsMemTotalPerNumaCgroup)
		if err != nil {
			return err
		}

		numaTotal, err := mg.metaServer.GetNumaMetric(numaID, consts.MetricMemTotalNuma)
		if err != nil {
			return err
		}
		numaFree, err := mg.metaServer.GetNumaMetric(numaID, consts.MetricMemFreeNuma)
		if err != nil {
			return err
		}

		criticalWatermark := math.Max(float64(mg.minCriticalWatermark), numaTotal.Value*watermarkScaleFactor.Value/float64(10000))
		reclaimMemoryLimit += reclaimedCoresUsed.Value +
			math.Max(numaFree.Value-criticalWatermark, 0)

		general.InfoS("NUMA memory info", "numaID", numaID,
			"criticalWatermark", general.FormatMemoryQuantity(criticalWatermark),
			"reclaimedCoresUsed", general.FormatMemoryQuantity(reclaimedCoresUsed.Value),
			"numaTotal", general.FormatMemoryQuantity(numaTotal.Value),
			"numaFree", general.FormatMemoryQuantity(numaFree.Value),
			"reclaimMemoryLimit", general.FormatMemoryQuantity(reclaimMemoryLimit))
	}

	mg.reclaimMemoryLimit.Store(int64(reclaimMemoryLimit))
	mg.reconcileStatus.Store(reconcileStatusSucceeded)

	return nil
}

func (mg *memoryGuard) GetAdvices() types.InternalMemoryCalculationResult {
	if mg.reconcileStatus.Load() == reconcileStatusFailed {
		general.Errorf("failed to get last reconcile result")
		return types.InternalMemoryCalculationResult{}
	}
	result := types.InternalMemoryCalculationResult{
		ExtraEntries: []types.ExtraMemoryAdvices{
			{
				CgroupPath: mg.reclaimRelativeRootCgroupPath,
				Values:     map[string]string{string(memoryadvisor.ControlKnobKeyMemoryLimitInBytes): strconv.FormatInt(mg.reclaimMemoryLimit.Load(), 10)},
			},
		},
	}

	return result
}
