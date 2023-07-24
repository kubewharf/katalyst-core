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
	"strconv"
	"sync"

	"go.uber.org/atomic"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	MemoryGuard = "memory-guard"
)

type memoryGuard struct {
	mutex                         sync.RWMutex
	metaReader                    metacache.MetaReader
	metaServer                    *metaserver.MetaServer
	emitter                       metrics.MetricEmitter
	reclaimRelativeRootCgroupPath string
	reclaimMemoryLimit            *atomic.Int64
}

func NewMemoryGuard(conf *config.Configuration, extraConfig interface{}, metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) MemoryAdvisorPlugin {
	return &memoryGuard{
		metaReader:                    metaReader,
		metaServer:                    metaServer,
		emitter:                       emitter,
		reclaimRelativeRootCgroupPath: conf.ReclaimRelativeRootCgroupPath,
		reclaimMemoryLimit:            atomic.NewInt64(-1),
	}
}

func (mg *memoryGuard) Reconcile(status *types.MemoryPressureStatus) error {
	memoryTotal, err := mg.metaServer.GetNodeMetric(consts.MetricMemTotalSystem)
	if err != nil {
		return err
	}

	memoryAvailable, err := mg.metaServer.GetNodeMetric(consts.MetricMemAvailableSystem)
	if err != nil {
		return err
	}

	scaleFactor, err := mg.metaServer.GetNodeMetric(consts.MetricMemScaleFactorSystem)
	if err != nil {
		return err
	}

	buffer := memoryAvailable.Value - memoryTotal.Value*scaleFactor.Value/10000
	if buffer < 0 {
		buffer = 0
	}

	reclaimGroupRss, err := mg.metaServer.GetCgroupMetric(mg.reclaimRelativeRootCgroupPath, consts.MetricMemRssCgroup)
	if err != nil {
		return err
	}

	general.Infof("memoryAvailable: %v, memoryTotal: %v, scaleFactor: %v, reclaimGroupRss: %v",
		resource.NewQuantity(int64(memoryAvailable.Value), resource.BinarySI).String(),
		resource.NewQuantity(int64(memoryTotal.Value), resource.BinarySI).String(),
		scaleFactor.Value,
		resource.NewQuantity(int64(reclaimGroupRss.Value), resource.BinarySI).String())

	mg.reclaimMemoryLimit.Store(int64(buffer + reclaimGroupRss.Value))

	return nil
}

func (mg *memoryGuard) GetAdvices() types.InternalMemoryCalculationResult {
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
