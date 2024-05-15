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

	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/provider"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
)

func StartCustomMetricServer(ctx context.Context, baseCtx *katalystbase.GenericContext, conf *config.Configuration,
	metricStore store.MetricStore,
) (func() error, func() error, error) {
	klog.Infoln("server is enabled")

	providerImp := provider.NewMetricProviderImp(ctx, baseCtx, metricStore)

	adapter := conf.Adapter
	adapter.WithCustomMetrics(providerImp)
	adapter.WithExternalMetrics(providerImp)

	start := func() error { return adapter.Run(ctx.Done()) }
	stop := func() error { return nil }
	return start, stop, nil
}
