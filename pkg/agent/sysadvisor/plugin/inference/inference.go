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

package inference

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher"
	borweinfetcher "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher/borwein"
	"github.com/kubewharf/katalyst-core/pkg/config"
	metricemitter "github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/metric-emitter"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func init() {
	modelresultfetcher.RegisterModelResultFetcherInitFunc(borweinfetcher.BorweinModelResultFetcherName,
		borweinfetcher.NewBorweinModelResultFetcher)
}

type InferencePlugin struct {
	name string
	// conf config.Configuration

	period               time.Duration
	modelsResultFetchers map[string]modelresultfetcher.ModelResultFetcher

	metaServer    *metaserver.MetaServer
	metricEmitter metrics.MetricEmitter

	emitterConf *metricemitter.MetricEmitterPluginConfiguration
	qosConf     *generic.QoSConfiguration

	metaReader metacache.MetaReader
	metaWriter metacache.MetaWriter
}

func NewInferencePlugin(pluginName string, conf *config.Configuration, extraConf interface{},
	emitterPool metricspool.MetricsEmitterPool, metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache,
) (plugin.SysAdvisorPlugin, error) {
	if conf == nil || conf.InferencePluginConfiguration == nil {
		return nil, fmt.Errorf("nil conf")
	} else if metaServer == nil {
		return nil, fmt.Errorf("nil metaServer")
	} else if metaCache == nil {
		return nil, fmt.Errorf("nil metaCache")
	} else if emitterPool == nil {
		return nil, fmt.Errorf("nil emitterPool")
	}

	metricEmitter := emitterPool.GetDefaultMetricsEmitter().WithTags("advisor-inference")

	inferencePlugin := InferencePlugin{
		name:                 pluginName,
		period:               conf.InferencePluginConfiguration.SyncPeriod,
		modelsResultFetchers: make(map[string]modelresultfetcher.ModelResultFetcher),
		metaServer:           metaServer,
		metricEmitter:        metricEmitter,
		emitterConf:          conf.AgentConfiguration.MetricEmitterPluginConfiguration,
		qosConf:              conf.GenericConfiguration.QoSConfiguration,
		metaReader:           metaCache,
		metaWriter:           metaCache,
	}

	for fetcherName, initFn := range modelresultfetcher.GetRegisteredModelResultFetcherInitFuncs() {
		// todo: support only enabling part of fetchers
		general.Infof("try init fetcher: %s", fetcherName)
		fetcher, err := initFn(fetcherName, conf, extraConf, emitterPool, metaServer, metaCache)
		if err != nil {
			return nil, fmt.Errorf("failed to start sysadvisor plugin %v: %v", pluginName, err)
		} else if fetcher == nil {
			general.Infof("fetcher: %s isn't enabled", fetcherName)
			continue
		}

		inferencePlugin.modelsResultFetchers[fetcherName] = fetcher
	}

	return &inferencePlugin, nil
}

func (infp *InferencePlugin) Run(ctx context.Context) {
	wait.UntilWithContext(ctx, infp.fetchModelResult, infp.period)
}

// Name returns the name of inference plugin
func (infp *InferencePlugin) Name() string {
	return infp.name
}

// Init initializes the inference plugin
func (infp *InferencePlugin) Init() error {
	return nil
}

func (infp *InferencePlugin) fetchModelResult(ctx context.Context) {
	var wg sync.WaitGroup

	for modelName, fetcher := range infp.modelsResultFetchers {
		wg.Add(1)
		go func(modelName string, fetcher modelresultfetcher.ModelResultFetcher) {
			defer wg.Done()
			general.Infof("FetchModelResult for model: %s start", modelName)
			err := fetcher.FetchModelResult(ctx, infp.metaReader, infp.metaWriter, infp.metaServer)
			if err != nil {
				general.Errorf("FetchModelResult for model: %s failed with error: %v", modelName, err)
			}
		}(modelName, fetcher)
	}

	wg.Wait()
}
