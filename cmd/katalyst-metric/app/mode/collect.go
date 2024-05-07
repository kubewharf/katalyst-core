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

package mode

import (
	"context"
	"fmt"
	"os"

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/collector"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/collector/prometheus"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/mock"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
)

func StartCustomMetricCollect(ctx context.Context, baseCtx *katalystbase.GenericContext, conf *config.Configuration,
	metricStore store.MetricStore,
) (func() error, func() error, error) {
	metricCollector, err := initMetricCollector(ctx, baseCtx, conf, metricStore)
	if err != nil {
		return nil, nil, fmt.Errorf("init collector failed: %v", err)
	}
	klog.Infoln("collector is enabled")

	id, err := os.Hostname()
	if err != nil {
		return nil, nil, fmt.Errorf("fail to get hostname: %v", err)
	}
	id = id + "_" + string(uuid.NewUUID())

	genericConf := conf.GenericMetricConfiguration
	rl, err := resourcelock.New(genericConf.LeaderElection.ResourceLock,
		genericConf.LeaderElection.ResourceNamespace,
		genericConf.LeaderElection.ResourceName,
		baseCtx.Client.KubeClient.CoreV1(),
		baseCtx.Client.KubeClient.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: baseCtx.BroadcastAdapter.DeprecatedNewLegacyRecorder(string(consts.KatalystComponentMetric)),
		})
	if err != nil {
		return nil, nil, fmt.Errorf("new resource lock: %v", err)
	}

	lCtx, cancel := context.WithCancel(ctx)
	start := func() error {
		f := func(collectCtx context.Context) {
			if err := metricCollector.Start(); err != nil {
				klog.Errorf("start metric collector failed: %v", err)
			}

			// if we lose leader election, we should call stop function;
			// and if it failed, we must be fatal to avoid concurrent collection
			for {
				select {
				case <-collectCtx.Done():
					if err := metricCollector.Stop(); err != nil {
						klog.Fatalf("stop metric collector failed: %v", err)
					}
				}
			}
		}

		leaderelection.RunOrDie(lCtx, leaderelection.LeaderElectionConfig{
			Lock:          rl,
			LeaseDuration: genericConf.LeaderElection.LeaseDuration.Duration,
			RenewDeadline: genericConf.LeaderElection.RenewDeadline.Duration,
			RetryPeriod:   genericConf.LeaderElection.RetryPeriod.Duration,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: f,
				OnStoppedLeading: func() {
					klog.Infof("loss leader lock.")
				},
			},
		})

		return nil
	}

	stop := func() error {
		cancel()
		return nil
	}

	return start, stop, nil
}

func initMetricCollector(ctx context.Context, baseCtx *katalystbase.GenericContext, conf *config.Configuration, metricStore store.MetricStore) (collector.MetricCollector, error) {
	switch conf.CollectorConfiguration.CollectorName {
	case prometheus.MetricCollectorNamePrometheus:
		return prometheus.NewPrometheusCollector(ctx, baseCtx, conf.GenericMetricConfiguration, conf.CollectorConfiguration, metricStore)
	case mock.MetricCollectorNameMock:
		return mock.NewMockCollector(ctx, baseCtx, conf.GenericMetricConfiguration, conf.CollectorConfiguration, conf.MockConfiguration, metricStore)
	}

	return nil, fmt.Errorf("unsupported collector name: %v", conf.CollectorConfiguration.CollectorName)
}
