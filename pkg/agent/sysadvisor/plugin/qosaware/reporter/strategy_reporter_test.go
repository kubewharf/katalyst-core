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

package reporter

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"k8s.io/utils/pointer"

	"github.com/stretchr/testify/require"

	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	nodeapis "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/strategygroup"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

func TestRunStrategyReporter(t *testing.T) {
	t.Parallel()

	regDir, ckDir, statDir, err := tmpDirs()
	require.NoError(t, err)
	defer func() {
		os.RemoveAll(regDir)
		os.RemoveAll(ckDir)
		os.RemoveAll(statDir)
	}()

	conf := generateTestConfiguration(t, regDir, ckDir, statDir)
	clientSet := generateTestGenericClientSet(nil, nil)
	metaServer := generateTestMetaServer(clientSet, conf)
	metaReader := generateTestMetaCache(t, conf, metaServer.MetricsFetcher)

	reporter, err := NewStrategyReporter(metrics.DummyMetrics{}, metaServer, metaReader, conf)

	require.NoError(t, err)
	require.NotNil(t, reporter)

	ctx, cancel := context.WithCancel(context.Background())
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		reporter.Run(ctx)
	}()
	time.Sleep(1 * time.Second)
	cancel()
	wg.Wait()
}

func TestStrategyUpdate(t *testing.T) {
	t.Parallel()

	regDir, ckDir, statDir, err := tmpDirs()
	require.NoError(t, err)
	defer func() {
		os.RemoveAll(regDir)
		os.RemoveAll(ckDir)
		os.RemoveAll(statDir)
	}()

	type args struct {
		conf *config.Configuration
	}

	tests := []struct {
		name           string
		args           args
		wantErr        bool
		wantStrategies []string
	}{
		{
			name: "update success",
			args: args{
				conf: func() *config.Configuration {
					globalConf := config.NewConfiguration()
					globalConf.SetDynamicConfiguration(&dynamic.Configuration{
						StrategyGroupConfiguration: &strategygroup.StrategyGroupConfiguration{
							EnabledStrategies: []configv1alpha1.Strategy{
								{
									Name: pointer.String("sa"),
								},
							},
						},
					})
					return globalConf
				}(),
			},
			wantErr:        false,
			wantStrategies: []string{"sa"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConfiguration(t, regDir, ckDir, statDir)

			conf.SetDynamicConfiguration(&dynamic.Configuration{
				StrategyGroupConfiguration: &strategygroup.StrategyGroupConfiguration{
					EnabledStrategies: []configv1alpha1.Strategy{
						{
							Name: pointer.String("sa"),
						},
					},
				},
			})

			clientSet := generateTestGenericClientSet(nil, nil)
			metaServer := generateTestMetaServer(clientSet, conf)
			metaReader := generateTestMetaCache(t, conf, metaServer.MetricsFetcher)

			reporter, err := NewStrategyReporter(metrics.DummyMetrics{}, metaServer, metaReader, conf)

			require.NoError(t, err)
			require.NotNil(t, reporter)

			fakeMetricsFetcher := metaServer.MetricsFetcher.(*metric.FakeMetricsFetcher)
			require.NotNil(t, fakeMetricsFetcher)
			reporterImpl, ok := reporter.(*StrategyReporterImpl)
			require.True(t, ok)
			pluginWrapper, ok := reporterImpl.GenericPlugin.(*skeleton.PluginRegistrationWrapper)
			require.True(t, ok)
			plugin, ok := pluginWrapper.GenericPlugin.(*StrategyReporterPlugin)
			require.True(t, ok)

			ctx, cancel := context.WithCancel(context.Background())
			wg := sync.WaitGroup{}
			wg.Add(1)

			go func() {
				defer wg.Done()
				reporter.Run(ctx)
			}()

			time.Sleep(2 * time.Second)

			resp, err := plugin.GetReportContent(context.TODO(), nil)
			if err != nil {
				require.True(t, tt.wantErr)
			} else {
				require.False(t, tt.wantErr)
				require.Equal(t, &util.CNRGroupVersionKind, resp.Content[0].GroupVersionKind)
				require.Equal(t, v1alpha1.FieldType_Spec, resp.Content[0].Field[0].FieldType)
				require.Equal(t, util.CNRFieldNameNodeResourceProperties, resp.Content[0].Field[0].FieldName)
				var res []*nodeapis.Property
				err = json.Unmarshal(resp.Content[0].Field[0].Value, &res)
				require.NoError(t, err)
				require.Len(t, res, 1)
				require.Equal(t, tt.wantStrategies, res[0].PropertyValues)
			}

			cancel()
			wg.Wait()
		})
	}
}
