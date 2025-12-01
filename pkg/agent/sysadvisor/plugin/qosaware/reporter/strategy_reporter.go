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
	"fmt"
	"sync"

	"k8s.io/klog/v2"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/strategygroup"
)

const (
	strategyReporterPluginName = "strategy-reporter-plugin"

	PropertyNameStrategy = "strategy"
)

// StrategyReporter report node strategies to node annotation
type StrategyReporter interface {
	Run(ctx context.Context)
}

type StrategyReporterImpl struct {
	skeleton.GenericPlugin
}

func NewStrategyReporter(
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
	_ metacache.MetaReader, conf *config.Configuration,
) (StrategyReporter, error) {
	plugin, err := newStrategyReporterPlugin(emitter, conf, metaServer)
	if err != nil {
		return nil, fmt.Errorf("[strategy-reporter] create reporter failed: %v", err)
	}

	return &StrategyReporterImpl{plugin}, nil
}

func (o *StrategyReporterImpl) Run(ctx context.Context) {
	if err := o.Start(); err != nil {
		klog.Fatalf("[strategy-reporter] start %v failed: %v", o.Name(), err)
	}
	klog.Infof("[strategy-reporter] plugin wrapper %v started", o.Name())

	<-ctx.Done()
	if err := o.Stop(); err != nil {
		klog.Errorf("[strategy-reporter] stop %v failed: %v", o.Name(), err)
	}
	klog.Infof("[strategy-reporter] stop")
}

type StrategyReporterPlugin struct {
	sync.Mutex

	metaServer *metaserver.MetaServer
	conf       *config.Configuration

	ctx     context.Context
	cancel  context.CancelFunc
	started bool
}

func newStrategyReporterPlugin(
	emitter metrics.MetricEmitter,
	conf *config.Configuration,
	metaserver *metaserver.MetaServer,
) (skeleton.GenericPlugin, error) {
	reporter := &StrategyReporterPlugin{
		metaServer: metaserver,
		conf:       conf,
	}

	return skeleton.NewRegistrationPluginWrapper(reporter, []string{conf.PluginRegistrationDir},
		func(key string, value int64) {
			_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
				"pluginName": strategyReporterPluginName,
				"pluginType": registration.ReporterPlugin,
			})...)
		})
}

func (o *StrategyReporterPlugin) Name() string {
	return strategyReporterPluginName
}

func (o *StrategyReporterPlugin) Start() (err error) {
	o.Lock()
	defer func() {
		if err == nil {
			o.started = true
		}
		o.Unlock()
	}()

	if o.started {
		return
	}

	o.ctx, o.cancel = context.WithCancel(context.Background())
	return
}

func (o *StrategyReporterPlugin) Stop() error {
	o.Lock()
	defer func() {
		o.started = false
		o.Unlock()
	}()

	if !o.started {
		return nil
	}

	o.cancel()
	return nil
}

// GetReportContent get Strategyment ratio from manager directly.
// Since the metrics collected by Manager are already an average within a time period,
// we expect a faster response to node load fluctuations to avoid excessive Strategy of online resources.
func (o *StrategyReporterPlugin) GetReportContent(ctx context.Context, _ *v1alpha1.Empty) (*v1alpha1.GetReportContentResponse, error) {
	response := &v1alpha1.GetReportContentResponse{
		Content: []*v1alpha1.ReportContent{},
	}

	// Strategy data to CNR data
	strategyContent, err := o.strategyToCNRProperty()
	if err != nil {
		return nil, err
	}
	response.Content = append(response.Content, strategyContent)

	return response, nil
}

func (o *StrategyReporterPlugin) ListAndWatchReportContent(_ *v1alpha1.Empty, server v1alpha1.ReporterPlugin_ListAndWatchReportContentServer) error {
	for {
		select {
		case <-o.ctx.Done():
			return nil
		case <-server.Context().Done():
			return nil
		}
	}
}

func (o *StrategyReporterPlugin) strategyToCNRProperty() (*v1alpha1.ReportContent, error) {
	strategiesForNode, err := strategygroup.GetEnabledStrategiesForNode(o.conf)
	if err != nil {
		return nil, err
	}
	general.Infof("report node strategies %v", strategiesForNode)
	var properties []*nodev1alpha1.Property

	properties = append(properties,
		&nodev1alpha1.Property{
			PropertyName:   PropertyNameStrategy,
			PropertyValues: strategiesForNode,
		},
	)

	value, err := json.Marshal(&properties)
	if err != nil {
		return nil, err
	}

	return &v1alpha1.ReportContent{
		GroupVersionKind: &util.CNRGroupVersionKind,
		Field: []*v1alpha1.ReportField{
			{
				FieldType: v1alpha1.FieldType_Spec,
				FieldName: util.CNRFieldNameNodeResourceProperties,
				Value:     value,
			},
		},
	}, nil
}
