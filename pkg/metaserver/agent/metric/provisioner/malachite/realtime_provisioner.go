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
	malachiteRealtimeProvisionTolerationTime = 5 * time.Second

	realtimePowerProvisioner                         = "malachite_realtime_power"
	metricsNameMalachiteRealtimePowerUnHealthy       = "malachite_realtime_power_unhealthy"
	malachiteRealtimePowerProvisionerHealthCheckName = "malachite_realtime_power_provisioner_sample"
	metricsNameMalachiteGetRealtimePowerFailed       = "malachite_get_realtime_power_failed"
)

func NewRealtimePowerMetricsProvisioner(baseConf *global.BaseConfiguration, _ *metaserver.MetricConfiguration,
	emitter metrics.MetricEmitter, fetcher pod.PodFetcher, metricStore *utilmetric.MetricStore, _ *machine.KatalystMachineInfo,
) types.MetricsProvisioner {
	inner := &MalachiteMetricsProvisioner{
		malachiteClient: client.NewMalachiteClient(fetcher, emitter),
		metricStore:     metricStore,
		emitter:         emitter,
		baseConf:        baseConf,
	}

	return &RealtimePowerMetricsProvisioner{
		dataGetter:                  inner.malachiteClient,
		MalachiteMetricsProvisioner: inner,
	}
}

type powerDataGetter interface {
	GetPowerData() (*malachitetypes.PowerData, error)
}

type sysFreqDataGetter interface {
	GetSysFreqData() (*malachitetypes.SysFreqData, error)
}

type RealtimePowerMetricsProvisioner struct {
	dataGetter powerDataGetter
	*MalachiteMetricsProvisioner
}

func (m *RealtimePowerMetricsProvisioner) Run(ctx context.Context) {
	m.startOnce.Do(func() {
		general.RegisterHeartbeatCheck(malachiteRealtimePowerProvisionerHealthCheckName,
			malachiteRealtimeProvisionTolerationTime,
			general.HealthzCheckStateNotReady,
			malachiteRealtimeProvisionTolerationTime)
	})
	m.sample(ctx)
}

func (m *RealtimePowerMetricsProvisioner) checkMalachiteHealthy() bool {
	_, err := m.dataGetter.GetPowerData()
	if err != nil {
		klog.Errorf("[%s] service is unhealthy: %v", realtimePowerProvisioner, err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteRealtimePowerUnHealthy, 1, metrics.MetricTypeNameRaw)
		return false
	}

	return true
}

func (m *RealtimePowerMetricsProvisioner) sample(ctx context.Context) {
	klog.V(4).Infof("[%s] heartbeat", realtimePowerProvisioner)

	// todo: consolidate check healthy and update with single get data call
	if !m.checkMalachiteHealthy() {
		_ = general.UpdateHealthzState(malachiteRealtimePowerProvisionerHealthCheckName,
			general.HealthzCheckStateNotReady, "malachite realtime power is not healthy")
		return
	}

	errList := make([]error, 0)
	if err := m.updateSystemTotalPower(); err != nil {
		errList = append(errList, err)
	}

	_ = general.UpdateHealthzStateByError(malachiteRealtimePowerProvisionerHealthCheckName, errors.NewAggregate(errList))
}

func (m *RealtimePowerMetricsProvisioner) updateSystemTotalPower() error {
	errList := make([]error, 0)
	powerData, err := m.dataGetter.GetPowerData()
	if err != nil {
		errList = append(errList, err)
		klog.Errorf("[%s] get power data failed, err %v", realtimePowerProvisioner, err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetRealtimePowerFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "power"})
	} else {
		m.processSystemPowerData(powerData)
	}

	return errors.NewAggregate(errList)
}

func (m *RealtimePowerMetricsProvisioner) processSystemPowerData(data *malachitetypes.PowerData) {
	if data == nil {
		return
	}

	updateTime := time.Unix(data.Sensors.UpdateTime, 0)
	m.metricStore.SetNodeMetric(consts.MetricTotalPowerUsedWatts,
		utilmetric.MetricData{Value: data.Sensors.TotalPowerWatt, Time: &updateTime})
}
