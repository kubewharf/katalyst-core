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

package app

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-metric/app/options"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/mock"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/custom"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/remote"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

// Run the metrics collector for katalyst.
func Run(opt *options.Options, genericOptions ...katalystbase.GenericOptions) error {
	conf, err := opt.Config()
	if err != nil {
		return err
	}

	clientSet, err := client.BuildGenericClient(conf.GenericConfiguration.ClientConnection, opt.MasterURL,
		opt.KubeConfig, fmt.Sprintf("%v", consts.KatalystComponentMetric))
	if err != nil {
		return err
	}

	// Set up signals so that we handle the first shutdown signal gracefully.
	ctx := process.SetupSignalHandler()

	baseCtx, err := katalystbase.NewGenericContext(clientSet, "", nil,
		sets.String{}, conf.GenericConfiguration, consts.KatalystComponentMetric, nil)
	if err != nil {
		return err
	}

	for _, genericOption := range genericOptions {
		genericOption(baseCtx)
	}

	baseCtx.Run(ctx)

	// start store, and it will be shared between other components
	metricStore, err := initStore(ctx, baseCtx, conf)
	if err != nil {
		klog.Fatalf("init metricStore %v failed: %v", metricStore.Name(), err)
	}

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer func() {
			if err := metricStore.Stop(); err != nil {
				klog.Errorf("stop metricStore %v failed: %v", metricStore.Name(), err)
			}

			wg.Done()
		}()

		klog.Infof("starting metricStore: %v", metricStore.Name())
		if err := metricStore.Start(); err != nil {
			klog.Fatalf("starting metricStore %v failed: %v", metricStore.Name(), err)
		}
	}()

	baseCtx.StartInformer(ctx)
	for workingMode, run := range GetMetricInitFuncMap() {
		if !baseCtx.IsEnabled(workingMode, conf.WorkMode) {
			klog.Warningf("%q is disabled", workingMode)
			continue
		}

		go wait.Until(func() {
			_ = baseCtx.EmitterPool.GetDefaultMetricsEmitter().StoreInt64("heart_beating", 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "component", Val: string(consts.KatalystComponentMetric)},
				metrics.MetricTag{Key: "mode", Val: workingMode},
			)
		}, 30*time.Second, ctx.Done())

		wg.Add(1)
		m := workingMode
		f := run

		go func() {
			start, stop, err := f(ctx, baseCtx, conf, metricStore)
			if err != nil {
				klog.Fatalf("starting %v failed: %v", m, err)
			}
			baseCtx.StartInformer(ctx)

			defer func() {
				klog.Infof("stopping %v", m)
				if err := stop(); err != nil {
					klog.Errorf("stop %v failed: %v", m, err)
				}

				wg.Done()
			}()
			klog.Infof("starting %v", m)

			if err := start(); err != nil {
				klog.Fatalf("starting %v failed: %v", m, err)
			}
		}()

		klog.Infof("started %q", m)
	}

	klog.Infof("custom metric cmd exiting, wait for goroutines exits.")
	wg.Wait()

	return nil
}

func initStore(ctx context.Context, baseCtx *katalystbase.GenericContext, conf *config.Configuration) (store.MetricStore, error) {
	go wait.Until(func() {
		_ = baseCtx.EmitterPool.GetDefaultMetricsEmitter().StoreInt64("kcmas_store", 1, metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "store_name", Val: conf.CustomMetricConfiguration.StoreConfiguration.StoreName},
		)
	}, 30*time.Second, ctx.Done())

	switch conf.CustomMetricConfiguration.StoreConfiguration.StoreName {
	case local.MetricStoreNameLocalMemory:
		return local.NewLocalMemoryMetricStore(ctx, baseCtx, conf.GenericMetricConfiguration, conf.StoreConfiguration)
	case mock.MetricStoreNameMockLocalMemory:
		// beware of using this store implementation. It's for pressure test only, never use it in any product environment
		// just replace the MetaInformer filled by mock data
		mock.ReplaceMetaInformerWithMockData(baseCtx, conf)
		return local.NewLocalMemoryMetricStore(ctx, baseCtx, conf.GenericMetricConfiguration, conf.StoreConfiguration)
	case remote.MetricStoreNameRemoteMemory:
		return remote.NewRemoteMemoryMetricStore(ctx, baseCtx, conf.GenericMetricConfiguration, conf.StoreConfiguration)
	case custom.SPDCustomMetricStore:
		return custom.NewSPDMetricStore(ctx, baseCtx, conf.GenericMetricConfiguration, conf.StoreConfiguration)
	}

	return nil, fmt.Errorf("unsupported store name: %v", conf.CustomMetricConfiguration.StoreConfiguration.StoreName)
}
