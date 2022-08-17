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
	"sync"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-metric/app/mode"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-metric/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
)

type metricInitFunc func(ctx context.Context, baseCtx *katalystbase.GenericContext, conf *config.Configuration,
	metricStore store.MetricStore) (func() error, func() error, error)

type MetricStarter struct {
	F metricInitFunc
}

var metricInitFuncMap sync.Map

func init() {
	metricInitFuncMap.Store(options.WorkModeCollector, MetricStarter{F: mode.StartCustomMetricCollect})
	metricInitFuncMap.Store(options.WorkModeProvider, MetricStarter{F: mode.StartCustomMetricServer})
	metricInitFuncMap.Store(options.WorkModeStoreServing, MetricStarter{F: mode.StartCustomMetricStoreServer})
}

func RegisterMetricInitFuncMap(name string, s MetricStarter) {
	metricInitFuncMap.Store(name, s)
}

func GetMetricInitFuncMap() map[string]metricInitFunc {
	initFuncMap := make(map[string]metricInitFunc)
	metricInitFuncMap.Range(func(key, value interface{}) bool {
		initFuncMap[key.(string)] = value.(MetricStarter).F
		return true
	})
	return initFuncMap
}
