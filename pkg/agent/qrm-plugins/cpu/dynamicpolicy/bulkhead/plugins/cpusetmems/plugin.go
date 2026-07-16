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

package cpusetmems

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	apierrors "k8s.io/apimachinery/pkg/util/errors"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	CPUSetMemsPluginName = "cpuset_mems"
	memsApplyMaxAttempts = 3
)

var (
	_                 bulkheadapi.Plugin = (*CPUSetMemsPlugin)(nil)
	errCPUSetMemsBusy                    = errors.New("cpuset.mems busy")
)

type memsApplyOrder string

const (
	memsPreOrder  memsApplyOrder = "pre"
	memsPostOrder memsApplyOrder = "post"
)

type CPUSetMemsPlugin struct {
	cfg    bulkheadconfig.BulkheadConfiguration
	cgroup cgroupclient.CgroupClient
}

func NewCPUSetMemsPlugin(conf *config.Configuration) bulkheadapi.Plugin {
	var cfg bulkheadconfig.BulkheadConfiguration
	if conf != nil && conf.CPUQRMPluginConfig != nil && conf.CPUQRMPluginConfig.BulkheadConfiguration != nil {
		cfg = *conf.CPUQRMPluginConfig.BulkheadConfiguration
	}
	return &CPUSetMemsPlugin{
		cfg:    cfg,
		cgroup: cgroupclient.NewCgroupClient(),
	}
}

func (p *CPUSetMemsPlugin) Name() string {
	return CPUSetMemsPluginName
}

func (p *CPUSetMemsPlugin) Enable(in bulkheadapi.HandlerContext) bool {
	return enableBulkheadCpusetMemsByDynamicConf(in.DynamicConf)
}

func (p *CPUSetMemsPlugin) CPUSetAdjustmentHandler(context.Context, bulkheadapi.HandlerContext) error {
	return nil
}

func (p *CPUSetMemsPlugin) CPUSetAdjustmentDisabledHandler(context.Context, bulkheadapi.HandlerContext) error {
	return nil
}

func (p *CPUSetMemsPlugin) PeriodicalHandler(ctx context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	version := p.cgroup.Version(ctx)
	numaIDs, err := numaIDsFromMetaServer(in.MetaServer)
	if err != nil {
		return err
	}
	if enableBulkheadCpusetMemsByDynamicConf(in.DynamicConf) {
		return p.reconcileNUMAMems(ctx, version, numaIDs)
	}
	return p.rollbackNUMAMems(ctx, version, numaIDs)
}

func enableBulkheadCpusetMemsByDynamicConf(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetMems
}

func numaIDsFromMetaServer(ms *metaserver.MetaServer) ([]int, error) {
	if ms == nil || ms.MetaAgent == nil || ms.CPUTopology == nil {
		return nil, fmt.Errorf("nil metaserver cpu topology for cpuset_mems")
	}
	numaIDs := ms.CPUTopology.CPUDetails.NUMANodes().ToSliceInt()
	if len(numaIDs) == 0 {
		return nil, fmt.Errorf("empty numa nodes for cpuset_mems")
	}
	return numaIDs, nil
}

func (p *CPUSetMemsPlugin) reconcileNUMAMems(ctx context.Context, version cgroupclient.CgroupVersion, numaIDs []int) error {
	var errs []error
	for reclaimIdx := range p.cfg.BulkheadReclaimRelPaths {
		for _, numaID := range numaIDs {
			rel := p.cfg.ReclaimPerNUMA(reclaimIdx, numaID)
			if rel == "" {
				continue
			}
			if _, err := p.cgroup.StatDir(ctx, rel); err != nil {
				general.InfofV(4, "bulkhead cpuset_mems: reclaim NUMA rel path does not exist, skipping, version=%s rel=%q err=%v", version, rel, err)
				continue
			}
			targetMems := strconv.Itoa(numaID)
			if p.rootMemsMatches(ctx, rel, targetMems) {
				continue
			}
			if err := p.applyMemsRecursive(ctx, rel, cgcommon.CPUSetData{Mems: targetMems}, memsPostOrder); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return apierrors.NewAggregate(errs)
}

func (p *CPUSetMemsPlugin) rollbackNUMAMems(ctx context.Context, version cgroupclient.CgroupVersion, numaIDs []int) error {
	var data cgcommon.CPUSetData
	if version == cgroupclient.CgroupVersionV2 {
		data = cgcommon.CPUSetData{Mems: "", WriteEmptyMems: true}
	} else {
		data = cgcommon.CPUSetData{Mems: joinNUMAIDs(numaIDs)}
	}

	var errs []error
	for reclaimIdx := range p.cfg.BulkheadReclaimRelPaths {
		for _, numaID := range numaIDs {
			rel := p.cfg.ReclaimPerNUMA(reclaimIdx, numaID)
			if rel == "" {
				continue
			}
			if _, err := p.cgroup.StatDir(ctx, rel); err != nil {
				general.InfofV(4, "bulkhead cpuset_mems: rollback rel path does not exist, skipping, version=%s rel=%q err=%v", version, rel, err)
				continue
			}
			if err := p.applyMemsRecursive(ctx, rel, data, memsPreOrder); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return apierrors.NewAggregate(errs)
}

func (p *CPUSetMemsPlugin) rootMemsMatches(ctx context.Context, rel, targetMems string) bool {
	data, err := p.cgroup.ReadCgroupFile(ctx, rel, "cpuset.mems")
	if err != nil {
		general.InfofV(5, "bulkhead cpuset_mems: read root cpuset.mems failed, falling back to recursive apply, rel=%q targetMems=%q err=%v", rel, targetMems, err)
		return false
	}
	return strings.TrimSpace(string(data)) == strings.TrimSpace(targetMems)
}

func (p *CPUSetMemsPlugin) applyMemsRecursive(ctx context.Context, rel string, data cgcommon.CPUSetData, order memsApplyOrder) error {
	var lastErr error
	for attempt := 1; attempt <= memsApplyMaxAttempts; attempt++ {
		err := p.applyMemsRecursiveOnce(ctx, rel, data, order)
		if err == nil {
			return nil
		}
		if !isRetryableMemsApplyError(err) {
			return err
		}
		lastErr = err
		general.InfofV(4, "bulkhead cpuset_mems: retry recursive mems apply after retryable error, rel=%q order=%s attempt=%d maxAttempts=%d err=%v",
			rel, order, attempt, memsApplyMaxAttempts, err)
	}
	general.InfofV(4, "bulkhead cpuset_mems: recursive mems apply still busy after retries, skipping without blocking admission, rel=%q order=%s attempts=%d err=%v",
		rel, order, memsApplyMaxAttempts, lastErr)
	return nil
}

func (p *CPUSetMemsPlugin) applyMemsRecursiveOnce(ctx context.Context, rel string, data cgcommon.CPUSetData, order memsApplyOrder) error {
	var errs []error
	if order == memsPreOrder {
		if err := p.applyMems(ctx, rel, data); err != nil {
			errs = append(errs, err)
		}
	}

	children, err := p.cgroup.ListChildren(ctx, rel)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("list cpuset_mems children @ %s: %w", rel, err))
		}
	} else {
		sort.Strings(children)
		for _, child := range children {
			childRel := filepath.Join(rel, child)
			if childErr := p.applyMemsRecursiveOnce(ctx, childRel, data, order); childErr != nil {
				errs = append(errs, childErr)
			}
		}
	}

	if order == memsPostOrder {
		if err := p.applyMems(ctx, rel, data); err != nil {
			errs = append(errs, err)
		}
	}
	return apierrors.NewAggregate(errs)
}

func (p *CPUSetMemsPlugin) applyMems(ctx context.Context, rel string, data cgcommon.CPUSetData) error {
	data.CPUs = ""
	data.Migrate = ""
	// Intentionally do not set CPUSetData.Migrate/cpuset.memory_migrate.
	// This plugin only changes cpuset.mems for future allocation locality.
	// Migrating existing pages is deferred to a separate design because it can
	// introduce latency spikes and kernel-specific behavior.
	if err := p.cgroup.ApplyCPUSet(ctx, rel, &data); err != nil {
		if isCPUSetMemsBusy(err) {
			return fmt.Errorf("%w: rel=%s mems=%s writeEmptyMems=%t: %v", errCPUSetMemsBusy, rel, data.Mems, data.WriteEmptyMems, err)
		}
		return fmt.Errorf("apply cpuset.mems=%s writeEmptyMems=%t @ %s: %w", data.Mems, data.WriteEmptyMems, rel, err)
	}
	return nil
}

func isRetryableMemsApplyError(err error) bool {
	return errors.Is(err, errCPUSetMemsBusy)
}

func isCPUSetMemsBusy(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EBUSY) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "device or resource busy")
}

func joinNUMAIDs(numaIDs []int) string {
	parts := make([]string, 0, len(numaIDs))
	for _, numaID := range numaIDs {
		parts = append(parts, strconv.Itoa(numaID))
	}
	return strings.Join(parts, ",")
}
