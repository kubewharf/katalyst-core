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
	metricsNamMalachiteRealtimeUnHealthy        = "malachite_realtime_unhealthy"
	malachiteRealtimeProvisionerHealthCheckName = "malachite_realtime_provisioner_sample"
	malachiteRealtimeProvisionTolerationTime    = 5 * time.Second
	metricsNameMalachiteGetRealtimePowerFailed  = "malachite_get_realtime_power_failed"

	metricsNameMalachiteGetRealtimeFreqFailed = "malachite_get_realtime_freq_failed"
)

func NewMalachiteRealtimeMetricsProvisioner(baseConf *global.BaseConfiguration, _ *metaserver.MetricConfiguration,
	emitter metrics.MetricEmitter, fetcher pod.PodFetcher, metricStore *utilmetric.MetricStore, _ *machine.KatalystMachineInfo,
) types.MetricsProvisioner {
	inner := &MalachiteMetricsProvisioner{
		malachiteClient: client.NewMalachiteClient(fetcher, emitter),
		metricStore:     metricStore,
		emitter:         emitter,
		baseConf:        baseConf,
	}

	return &MalachiteRealtimeMetricsProvisioner{
		malachiteClient:             inner.malachiteClient,
		MalachiteMetricsProvisioner: inner,
	}
}

type realtimeDataGetter interface {
	powerDataGetter
	sysFreqDataGetter
}

type powerDataGetter interface {
	GetPowerData() (*malachitetypes.PowerData, error)
}

type sysFreqDataGetter interface {
	GetSysFreqData() (*malachitetypes.SysFreqData, error)
}

type MalachiteRealtimeMetricsProvisioner struct {
	malachiteClient realtimeDataGetter
	*MalachiteMetricsProvisioner
}

func (m *MalachiteRealtimeMetricsProvisioner) Run(ctx context.Context) {
	m.startOnce.Do(func() {
		general.RegisterHeartbeatCheck(malachiteRealtimeProvisionerHealthCheckName,
			malachiteRealtimeProvisionTolerationTime,
			general.HealthzCheckStateNotReady,
			malachiteRealtimeProvisionTolerationTime)
	})
	m.sample(ctx)
}

func (m *MalachiteRealtimeMetricsProvisioner) checkMalachiteHealthy() bool {
	_, err := m.malachiteClient.GetPowerData()
	if err != nil {
		klog.Errorf("[malachite_realtime] malachite realtime is unhealthy: %v", err)
		_ = m.emitter.StoreInt64(metricsNamMalachiteRealtimeUnHealthy, 1, metrics.MetricTypeNameRaw)
		return false
	}

	return true
}

func (m *MalachiteRealtimeMetricsProvisioner) sample(ctx context.Context) {
	klog.V(4).Infof("[malachite_realtime] heartbeat")

	if !m.checkMalachiteHealthy() {
		_ = general.UpdateHealthzState(malachiteRealtimeProvisionerHealthCheckName, general.HealthzCheckStateNotReady, "malachite realtime is not healthy")
		return
	}
	errList := make([]error, 0)

	// Update system data
	if err := m.updateSystemTotalPower(); err != nil {
		errList = append(errList, err)
	}
	if err := m.updateSystemCPUFreq(); err != nil {
		errList = append(errList, err)
	}

	_ = general.UpdateHealthzStateByError(malachiteRealtimeProvisionerHealthCheckName, errors.NewAggregate(errList))
}

func (m *MalachiteRealtimeMetricsProvisioner) updateSystemTotalPower() error {
	errList := make([]error, 0)
	powerData, err := m.malachiteClient.GetPowerData()
	if err != nil {
		errList = append(errList, err)
		klog.Errorf("[malachite-realtime] get power data failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetRealtimePowerFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "power"})
	} else {
		m.processSystemPowerData(powerData)
	}

	return errors.NewAggregate(errList)
}

func (m *MalachiteRealtimeMetricsProvisioner) processSystemPowerData(data *malachitetypes.PowerData) {
	if data == nil {
		return
	}

	updateTime := time.Unix(data.Sensors.UpdateTime, 0)
	m.metricStore.SetNodeMetric(consts.MetricTotalPowerUsedWatts,
		utilmetric.MetricData{Value: data.Sensors.TotalPowerWatt, Time: &updateTime})
}

func (m *MalachiteRealtimeMetricsProvisioner) updateSystemCPUFreq() error {
	errList := make([]error, 0)
	data, err := m.malachiteClient.GetSysFreqData()
	if err != nil {
		errList = append(errList, err)
		klog.Errorf("[malachite-realtime] get cpufreq data failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetRealtimeFreqFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "power"})
	} else {
		m.processSystemCPUFreqData(data)
	}

	return errors.NewAggregate(errList)
}

func (m *MalachiteRealtimeMetricsProvisioner) processSystemCPUFreqData(data *malachitetypes.SysFreqData) {
	if data == nil {
		return
	}

	cpuFreqKHZ, err := getScalingCurFreq(data)
	if err != nil {
		general.Warningf("pap: got empty scaling_cur_freq")
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

	// always use 0 element
	return &cpuFreqs[0], nil
}
