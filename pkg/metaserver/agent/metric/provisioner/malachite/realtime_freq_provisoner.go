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

package malachite

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/client"
	malachitetypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	realtimeFreqProvisioner                         = "malachite_realtime_freq"
	metricsNameMalachiteRealtimeFreqUnHealthy       = "malachite_realtime_freq_unhealthy"
	malachiteRealtimeFreqProvisionerHealthCheckName = "malachite_realtime_freq_provisioner_sample"
	metricsNameMalachiteGetRealtimeFreqFailed       = "malachite_get_realtime_freq_failed"
)

func NewRealtimeFreqMetricsProvisioner(baseConf *global.BaseConfiguration, _ *metaserver.MetricConfiguration,
	emitter metrics.MetricEmitter, fetcher pod.PodFetcher, metricStore *utilmetric.MetricStore, _ *machine.KatalystMachineInfo,
) types.MetricsProvisioner {
	inner := &MalachiteMetricsProvisioner{
		malachiteClient: client.NewMalachiteClient(fetcher, emitter),
		metricStore:     metricStore,
		emitter:         emitter,
		baseConf:        baseConf,
	}

	return &RealtimeFreqMetricsProvisioner{
		dataGetter:                  inner.malachiteClient,
		MalachiteMetricsProvisioner: inner,
	}
}

type RealtimeFreqMetricsProvisioner struct {
	dataGetter sysFreqDataGetter
	*MalachiteMetricsProvisioner
}

func (m *RealtimeFreqMetricsProvisioner) Run(ctx context.Context) {
	m.startOnce.Do(func() {
		general.RegisterHeartbeatCheck(malachiteRealtimeFreqProvisionerHealthCheckName,
			malachiteRealtimeProvisionTolerationTime,
			general.HealthzCheckStateNotReady,
			malachiteRealtimeProvisionTolerationTime)
	})
	m.sample(ctx)
}

func (m *RealtimeFreqMetricsProvisioner) sample(ctx context.Context) {
	klog.V(4).Infof("[%s] heartbeat", realtimeFreqProvisioner)

	// todo: consolidate check healthy and update with single get data call
	if !m.checkMalachiteHealthy() {
		_ = general.UpdateHealthzState(malachiteRealtimeFreqProvisionerHealthCheckName,
			general.HealthzCheckStateNotReady, "malachite realtime freq is not healthy")
		return
	}

	errList := make([]error, 0)
	if err := m.updateSystemCPUFreq(); err != nil {
		errList = append(errList, err)
	}

	_ = general.UpdateHealthzStateByError(malachiteRealtimeFreqProvisionerHealthCheckName, errors.NewAggregate(errList))
}

func (m *RealtimeFreqMetricsProvisioner) checkMalachiteHealthy() bool {
	_, err := m.dataGetter.GetSysFreqData()
	if err != nil {
		klog.Errorf("[%s] service is unhealthy: %v", realtimeFreqProvisioner, err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteRealtimeFreqUnHealthy, 1, metrics.MetricTypeNameRaw)
		return false
	}

	return true
}

func (m *RealtimeFreqMetricsProvisioner) updateSystemCPUFreq() error {
	errList := make([]error, 0)
	data, err := m.dataGetter.GetSysFreqData()
	if err != nil {
		errList = append(errList, err)
		klog.Errorf("[%s] get cpufreq data failed, err %v", realtimeFreqProvisioner, err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetRealtimeFreqFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "power"})
	} else {
		m.processSystemCPUFreqData(data)
	}

	return errors.NewAggregate(errList)
}

func (m *RealtimeFreqMetricsProvisioner) processSystemCPUFreqData(data *malachitetypes.SysFreqData) {
	if data == nil {
		return
	}

	cpuFreqKHZ, err := getScalingCurFreq(data)
	if err != nil {
		general.Warningf("[%s] pap: failed to get freq: %v", realtimeFreqProvisioner, err)
		return
	}

	updateTime := time.Unix(data.SysFreq.UpdateTime, 0)
	m.metricStore.SetNodeMetric(consts.MetricScalingCPUFreqKHZ,
		utilmetric.MetricData{Value: float64(cpuFreqKHZ.ScalingCurFreqKHZ), Time: &updateTime})
}

func getScalingCurFreq(data *malachitetypes.SysFreqData) (*malachitetypes.CurFreq, error) {
	cpuFreqs := data.SysFreq.CPUFreq
	if len(cpuFreqs) == 0 {
		return nil, fmt.Errorf("empty cpufreq data")
	}

	// always use element 0
	return &cpuFreqs[0], nil
}
