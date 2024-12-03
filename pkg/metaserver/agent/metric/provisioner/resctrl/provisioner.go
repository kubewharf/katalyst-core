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

package resctrl

import (
	"context"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	metricsNamResctrlMBSummaryUnHealthy = "resctrl_mb_summary_unhealthy"
)

// NewResctrlMetricsProvisioner returns the default implementation of MetricsFetcher.
func NewResctrlMetricsProvisioner(baseConf *global.BaseConfiguration, _ *metaserver.MetricConfiguration,
	emitter metrics.MetricEmitter, fetcher pod.PodFetcher, metricStore *utilmetric.MetricStore,
) types.MetricsProvisioner {
	dataKeeper, err := state.NewMBRawDataKeeper()
	if err != nil {
		general.Errorf("mbm: failed to create raw data state keeper: %v", err)
		return nil
	}

	dieTopology := machine.HostDieTopology
	incubationInterval := monitor.IncubationInterval
	domainManager := mbdomain.NewMBDomainManager(dieTopology, incubationInterval)

	podMBMonitor, err := monitor.NewDefaultMBMonitor(dieTopology.CPUsInDie, dataKeeper, domainManager)
	if err != nil {
		general.Errorf("mbm: failed to create default mb monitor: %v", err)
	}

	return &resctrlMetricsProvisioner{
		metricStore: metricStore,
		emitter:     emitter,
		mbReader:    podMBMonitor,
	}
}

type resctrlMetricsProvisioner struct {
	metricStore *utilmetric.MetricStore
	emitter     metrics.MetricEmitter
	mbReader    monitor.MBMonitor
}

func (r *resctrlMetricsProvisioner) Run(ctx context.Context) {
	r.sample(ctx)
}

func (r *resctrlMetricsProvisioner) sample(ctx context.Context) {
	qosCCDMB, err := r.mbReader.GetMBQoSGroups()
	if err != nil {
		general.Errorf("mbm: failed to get MB usages: %v", err)
		_ = r.emitter.StoreInt64(metricsNamResctrlMBSummaryUnHealthy, 1, metrics.MetricTypeNameRaw)
		return
	}

	general.InfofV(6, "mbm: mb usage summary: %v", monitor.DisplayMBSummary(qosCCDMB))
	r.processTotalMB(qosCCDMB)
	// todo: save read/write MB metrics
}

func (r *resctrlMetricsProvisioner) processTotalMB(qosCCDMB map[qosgroup.QoSGroup]*monitor.MBQoSGroup) {
	if qosCCDMB == nil {
		return
	}

	updateTime := time.Now()

	// 2-dimensional indices by qos group x ccd
	newMetricBlob := make(map[string]map[int]utilmetric.MetricData)
	for qosGroup, data := range qosCCDMB {
		newMetricBlob[string(qosGroup)] = make(map[int]utilmetric.MetricData)

		for ccd, mb := range data.CCDMB {
			newMetricBlob[string(qosGroup)][ccd] = utilmetric.MetricData{
				Value: float64(mb.ReadsMB + mb.WritesMB),
				Time:  &updateTime,
			}
		}
	}
	r.metricStore.SetByStringIndex(consts.MetricTotalMemBandwidthQoSGroup, newMetricBlob)
}
