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
	"testing"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher"
	borweinfetcher "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/modelresultfetcher/borwein"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func NewNilModelResultFetcher(fetcherName string, conf *config.Configuration, extraConf interface{},
	emitterPool metricspool.MetricsEmitterPool, metaServer *metaserver.MetaServer,
	metaCache metacache.MetaCache,
) (modelresultfetcher.ModelResultFetcher, error) {
	return nil, nil
}

func TestNewInferencePlugin(t *testing.T) {
	t.Parallel()
	type args struct {
		pluginName  string
		conf        *config.Configuration
		extraConf   interface{}
		emitterPool metricspool.MetricsEmitterPool
		metaServer  *metaserver.MetaServer
		metaCache   metacache.MetaCache
	}
	conf := config.NewConfiguration()
	conf.InferencePluginConfiguration.SyncPeriod = 5 * time.Second
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "test new inference plugin",
			args: args{
				pluginName:  types.AdvisorPluginNameInference,
				conf:        conf,
				emitterPool: metricspool.DummyMetricsEmitterPool{},
				metaServer:  &metaserver.MetaServer{},
				metaCache:   &metacache.MetaCacheImp{},
			},
			wantErr: false,
		},
	}

	modelresultfetcher.RegisterModelResultFetcherInitFunc(borweinfetcher.BorweinModelResultFetcherName,
		modelresultfetcher.NewDummyModelResultFetcher)
	modelresultfetcher.RegisterModelResultFetcherInitFunc("test-nil-fetcher",
		NewNilModelResultFetcher)

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewInferencePlugin(tt.args.pluginName, tt.args.conf, tt.args.extraConf, tt.args.emitterPool, tt.args.metaServer, tt.args.metaCache)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewInferencePlugin() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func TestInferencePlugin_Run(t *testing.T) {
	t.Parallel()
	type fields struct {
		name                 string
		period               time.Duration
		modelsResultFetchers map[string]modelresultfetcher.ModelResultFetcher
		metaServer           *metaserver.MetaServer
		emitter              metrics.MetricEmitter
		metaReader           metacache.MetaReader
		metaWriter           metacache.MetaWriter
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name: "test running inference plugin",
			fields: fields{
				name:   types.AdvisorPluginNameInference,
				period: 5 * time.Second,
				modelsResultFetchers: map[string]modelresultfetcher.ModelResultFetcher{
					"test": modelresultfetcher.DummyModelResultFetcher{},
				},
				metaServer: &metaserver.MetaServer{},
				emitter:    metrics.DummyMetrics{},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var cancel context.CancelFunc
			tt.args.ctx, cancel = context.WithCancel(context.Background())
			infp := &InferencePlugin{
				name:                 tt.fields.name,
				period:               tt.fields.period,
				modelsResultFetchers: tt.fields.modelsResultFetchers,
				metaServer:           tt.fields.metaServer,
				metricEmitter:        tt.fields.emitter,
				metaReader:           tt.fields.metaReader,
				metaWriter:           tt.fields.metaWriter,
			}
			go infp.Run(tt.args.ctx)
			cancel()
		})
	}
}

func TestInferencePlugin_Name(t *testing.T) {
	t.Parallel()
	type fields struct {
		name                 string
		period               time.Duration
		modelsResultFetchers map[string]modelresultfetcher.ModelResultFetcher
		metaServer           *metaserver.MetaServer
		emitter              metrics.MetricEmitter
		metaReader           metacache.MetaReader
		metaWriter           metacache.MetaWriter
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "test inference plugin name",
			fields: fields{
				name:   types.AdvisorPluginNameInference,
				period: 5 * time.Second,
				modelsResultFetchers: map[string]modelresultfetcher.ModelResultFetcher{
					"test": modelresultfetcher.DummyModelResultFetcher{},
				},
				metaServer: &metaserver.MetaServer{},
				emitter:    metrics.DummyMetrics{},
			},
			want: types.AdvisorPluginNameInference,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			infp := &InferencePlugin{
				name:                 tt.fields.name,
				period:               tt.fields.period,
				modelsResultFetchers: tt.fields.modelsResultFetchers,
				metaServer:           tt.fields.metaServer,
				metricEmitter:        tt.fields.emitter,
				metaReader:           tt.fields.metaReader,
				metaWriter:           tt.fields.metaWriter,
			}
			if got := infp.Name(); got != tt.want {
				t.Errorf("InferencePlugin.Name() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInferencePlugin_Init(t *testing.T) {
	t.Parallel()
	type fields struct {
		name                 string
		period               time.Duration
		modelsResultFetchers map[string]modelresultfetcher.ModelResultFetcher
		metaServer           *metaserver.MetaServer
		emitter              metrics.MetricEmitter
		metaReader           metacache.MetaReader
		metaWriter           metacache.MetaWriter
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "test initializing inference plugin",
			fields: fields{
				name:   types.AdvisorPluginNameInference,
				period: 5 * time.Second,
				modelsResultFetchers: map[string]modelresultfetcher.ModelResultFetcher{
					"test": modelresultfetcher.DummyModelResultFetcher{},
				},
				metaServer: &metaserver.MetaServer{},
				emitter:    metrics.DummyMetrics{},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			infp := &InferencePlugin{
				name:                 tt.fields.name,
				period:               tt.fields.period,
				modelsResultFetchers: tt.fields.modelsResultFetchers,
				metaServer:           tt.fields.metaServer,
				metricEmitter:        tt.fields.emitter,
				metaReader:           tt.fields.metaReader,
				metaWriter:           tt.fields.metaWriter,
			}
			if err := infp.Init(); (err != nil) != tt.wantErr {
				t.Errorf("InferencePlugin.Init() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestInferencePlugin_fetchModelResult(t *testing.T) {
	t.Parallel()
	type fields struct {
		name                 string
		period               time.Duration
		modelsResultFetchers map[string]modelresultfetcher.ModelResultFetcher
		metaServer           *metaserver.MetaServer
		emitter              metrics.MetricEmitter
		metaReader           metacache.MetaReader
		metaWriter           metacache.MetaWriter
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name: "test inference plugin fetching model result",
			fields: fields{
				name:   types.AdvisorPluginNameInference,
				period: 5 * time.Second,
				modelsResultFetchers: map[string]modelresultfetcher.ModelResultFetcher{
					"test": modelresultfetcher.DummyModelResultFetcher{},
				},
				metaServer: &metaserver.MetaServer{},
				emitter:    metrics.DummyMetrics{},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			infp := &InferencePlugin{
				name:                 tt.fields.name,
				period:               tt.fields.period,
				modelsResultFetchers: tt.fields.modelsResultFetchers,
				metaServer:           tt.fields.metaServer,
				metricEmitter:        tt.fields.emitter,
				metaReader:           tt.fields.metaReader,
				metaWriter:           tt.fields.metaWriter,
			}
			infp.fetchModelResult(tt.args.ctx)
		})
	}
}
