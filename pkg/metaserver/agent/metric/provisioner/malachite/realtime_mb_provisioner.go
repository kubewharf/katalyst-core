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
	metricsNamMalachiteRealtimeMBUnHealthy        = "malachite_realtime_mb_unhealthy"
	malachiteRealtimeMBProvisionerHealthCheckName = "malachite_realtime_mb_provisioner_sample"
	malachiteRealtimeMBProvisionTolerationTime    = 5 * time.Second
	metricsNameMalachiteGetRealtimeMBFailed       = "malachite_get_realtime_mb_failed"
)

type mbDataGetter interface {
	GetMBData() (*malachitetypes.MBData, error)
}

type MalachiteRealtimeMBMetricsProvisioner struct {
	malachiteClient mbDataGetter
	*MalachiteMetricsProvisioner
}

func (m *MalachiteRealtimeMBMetricsProvisioner) Run(ctx context.Context) {
	m.startOnce.Do(func() {
		general.RegisterHeartbeatCheck(malachiteRealtimeMBProvisionerHealthCheckName,
			malachiteRealtimeMBProvisionTolerationTime,
			general.HealthzCheckStateNotReady,
			malachiteRealtimeMBProvisionTolerationTime)
	})
	m.sample(ctx)
}

func (m *MalachiteRealtimeMBMetricsProvisioner) sample(ctx context.Context) {
	if klog.V(6).Enabled() {
		general.Infof("[malachite_realtime_mb] heartbeat")
	}

	if !m.checkMalachiteHealthy() {
		_ = general.UpdateHealthzState(malachiteRealtimeMBProvisionerHealthCheckName, general.HealthzCheckStateNotReady, "malachite realtime is not healthy")
		return
	}

	errList := make([]error, 0)
	if err := m.updateMB(); err != nil {
		errList = append(errList, err)
	}

	_ = general.UpdateHealthzStateByError(malachiteRealtimeMBProvisionerHealthCheckName, errors.NewAggregate(errList))
}

func (m *MalachiteRealtimeMBMetricsProvisioner) checkMalachiteHealthy() bool {
	_, err := m.malachiteClient.GetMBData()
	if err != nil {
		klog.Errorf("[malachite_realtime_mb] malachite realtime/mb is unhealthy: %v", err)
		_ = m.emitter.StoreInt64(metricsNamMalachiteRealtimeMBUnHealthy, 1, metrics.MetricTypeNameRaw)
		return false
	}

	return true
}

func (m *MalachiteRealtimeMBMetricsProvisioner) updateMB() error {
	errList := make([]error, 0)
	data, err := m.malachiteClient.GetMBData()
	if err != nil {
		errList = append(errList, err)
		klog.Errorf("[malachite_realtime_mb] get realtime/mb failed, err %v", err)
		_ = m.emitter.StoreInt64(metricsNameMalachiteGetRealtimeMBFailed, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: "mb"})
	} else {
		m.processMBData(data)
	}

	return errors.NewAggregate(errList)
}

func (m *MalachiteRealtimeMBMetricsProvisioner) processMBData(data *malachitetypes.MBData) {
	if data == nil {
		return
	}
	// data already contains timestamp, no need to put one wrapped in a MetricData
	m.metricStore.SetByStringIndex(consts.MetricRealtimeMB, data)
}

func NewMalachiteRealtimeMBMetricsProvisioner(baseConf *global.BaseConfiguration, _ *metaserver.MetricConfiguration,
	emitter metrics.MetricEmitter, fetcher pod.PodFetcher, metricStore *utilmetric.MetricStore, _ *machine.KatalystMachineInfo,
) types.MetricsProvisioner {
	inner := &MalachiteMetricsProvisioner{
		malachiteClient: client.NewMalachiteClient(fetcher, emitter),
		metricStore:     metricStore,
		emitter:         emitter,
		baseConf:        baseConf,
	}

	return &MalachiteRealtimeMBMetricsProvisioner{
		malachiteClient:             inner.malachiteClient,
		MalachiteMetricsProvisioner: inner,
	}
}
