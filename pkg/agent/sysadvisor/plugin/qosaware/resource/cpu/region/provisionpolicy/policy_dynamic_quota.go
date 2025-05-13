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

package provisionpolicy

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/errors"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PolicyDynamicQuota struct {
	*PolicyBase
	conf *config.Configuration
}

func NewPolicyDynamicQuota(regionName string, regionType configapi.QoSRegionType, ownerPoolName string,
	conf *config.Configuration, _ interface{}, metaReader metacache.MetaReader,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) ProvisionPolicy {
	p := &PolicyDynamicQuota{
		PolicyBase: NewPolicyBase(regionName, regionType, ownerPoolName, metaReader, metaServer, emitter),
		conf:       conf,
	}
	return p
}

func (p *PolicyDynamicQuota) isCPUQuotaAsControlKnob() bool {
	if !common.CheckCgroup2UnifiedMode() || !p.isNUMABinding {
		return false
	}

	_, ok := p.Indicators[string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio)]
	return ok
}

func (p *PolicyDynamicQuota) updateForCPUQuota() error {
	indicator := p.Indicators[string(workloadv1alpha1.ServiceSystemIndicatorNameCPUUsageRatio)]
	reclaimPath := common.GetReclaimRelativeRootCgroupPath(p.conf.ReclaimRelativeRootCgroupPath, p.bindingNumas.ToSliceInt()[0])
	data, err := p.metaServer.GetCgroupMetric(reclaimPath, pkgconsts.MetricCPUUsageCgroup)
	if err != nil {
		return err
	}
	reclaimCoresCPUUsage := data.Value

	cpusPerNUMA := p.metaServer.CPUsPerNuma()
	if cpusPerNUMA == 0 {
		return fmt.Errorf("invalid cpu count per numa: %d, %d", p.metaServer.NumNUMANodes, p.metaServer.NumCPUs)
	}
	quota := general.MaxFloat64(float64(cpusPerNUMA)*(indicator.Target-indicator.Current)+reclaimCoresCPUUsage, p.ReservedForReclaim)

	general.InfoS("metrics", "cpuUsage", reclaimCoresCPUUsage, "cpusPerNUMA", cpusPerNUMA, "target", indicator.Target, "current", indicator.Current, "quota", quota, "numas", p.bindingNumas.String())

	p.controlKnobAdjusted = types.ControlKnob{
		configapi.ControlKnobReclaimedCoresCPUQuota: types.ControlKnobItem{
			Value:  quota,
			Action: types.ControlKnobActionNone,
		},
	}
	return nil
}

func (p *PolicyDynamicQuota) Update() error {
	// sanity check
	if err := p.sanityCheck(); err != nil {
		return err
	}

	if p.isCPUQuotaAsControlKnob() {
		return p.updateForCPUQuota()
	}

	return nil
}

func (p *PolicyDynamicQuota) sanityCheck() error {
	var (
		isLegal bool
		errList []error
	)

	// 1. check control knob legality
	isLegal = true
	if p.ControlKnobs != nil {
		v, ok := p.ControlKnobs[configapi.ControlKnobReclaimedCoresCPUQuota]
		if !ok || v.Value <= 0 {
			isLegal = false
		}
	}
	if !isLegal {
		errList = append(errList, fmt.Errorf("illegal control knob %v", p.ControlKnobs))
	}

	return errors.NewAggregate(errList)
}
