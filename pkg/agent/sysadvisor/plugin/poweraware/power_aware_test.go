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

package poweraware

import (
	"context"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action/strategy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/advisor/action/strategy/assess"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/evictor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/reader"
	"github.com/kubewharf/katalyst-core/pkg/config"
	agentconf "github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/poweraware"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

type stubNodeFetcher struct {
	node.NodeFetcher
}

func Test_powerAwarePlugin_Name(t *testing.T) {
	t.Parallel()

	expectedName := "test"
	expectedDryRun := true

	stubMetaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			NodeFetcher: &stubNodeFetcher{},
		},
	}
	dummyPluginConf := poweraware.PowerAwarePluginConfiguration{
		DryRun: expectedDryRun,
	}
	dummyEmitterPool := metricspool.DummyMetricsEmitterPool{}

	percentageEvictor := evictor.NewPowerLoadEvict(nil,
		dummyEmitterPool.GetDefaultMetricsEmitter(),
		stubMetaServer.PodFetcher,
		evictor.NewNoopPodEvictor(),
	)

	strategy := strategy.NewEvictFirstStrategy(dummyEmitterPool.GetDefaultMetricsEmitter(),
		percentageEvictor,
		nil,
		nil,
		assess.NewPowerChangeAssessor(0, 0),
	)
	reconciler := advisor.NewReconciler(expectedDryRun,
		dummyEmitterPool.GetDefaultMetricsEmitter(),
		percentageEvictor,
		nil,
		strategy,
	)

	stubAdvisor := advisor.NewAdvisor(
		expectedDryRun,
		"foo",
		evictor.NewNoopPodEvictor(),
		dummyEmitterPool.GetDefaultMetricsEmitter(),
		stubMetaServer.NodeFetcher,
		nil,
		nil,
		reconciler,
	)

	p, err := newPluginWithAdvisor(expectedName,
		&config.Configuration{
			AgentConfiguration: &agentconf.AgentConfiguration{
				StaticAgentConfiguration: &agentconf.StaticAgentConfiguration{
					SysAdvisorPluginsConfiguration: &sysadvisor.SysAdvisorPluginsConfiguration{
						PowerAwarePluginConfiguration: &dummyPluginConf,
					},
				},
			},
			GenericConfiguration: &generic.GenericConfiguration{
				QoSConfiguration: generic.NewQoSConfiguration(),
			},
		},
		stubAdvisor,
	)
	if err != nil {
		t.Errorf("unexpected error: %#v", err)
	}

	if p.Name() != expectedName {
		t.Errorf("expected %s, got %s", expectedName, p.Name())
	}

	pap := p.(*powerAwarePlugin)
	if pap.dryRun != expectedDryRun {
		t.Errorf("expected dryrun %v, got %v", expectedDryRun, pap.dryRun)
	}
}

func Test_powerAwarePlugin_Init(t *testing.T) {
	t.Parallel()
	dummyEmitter := metricspool.DummyMetricsEmitterPool{}.GetDefaultMetricsEmitter().WithTags("advisor-poweraware")

	type fields struct {
		name        string
		disabled    bool
		dryRun      bool
		nodeFetcher node.NodeFetcher
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "happy path no error",
			fields: fields{
				name:     "dummy",
				disabled: false,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			percentageEvictor := evictor.NewPowerLoadEvict(nil,
				dummyEmitter,
				nil,
				evictor.NewNoopPodEvictor(),
			)
			strategy := strategy.NewEvictFirstStrategy(dummyEmitter,
				percentageEvictor,
				nil,
				nil,
				assess.NewPowerChangeAssessor(0, 0),
			)
			reconciler := advisor.NewReconciler(false,
				dummyEmitter,
				percentageEvictor,
				nil,
				strategy,
			)
			p := powerAwarePlugin{
				name:   tt.fields.name,
				dryRun: tt.fields.dryRun,
				advisor: advisor.NewAdvisor(false,
					"bar",
					evictor.NewNoopPodEvictor(),
					dummyEmitter,
					tt.fields.nodeFetcher,
					reader.NewDummyPowerReader(),
					capper.NewNoopCapper(),
					reconciler,
				),
			}
			if err := p.Init(); (err != nil) != tt.wantErr {
				t.Errorf("Init() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

type dummyAdvisor struct {
	advisor.PowerAwareAdvisor
	called bool
}

func (d *dummyAdvisor) Run(ctx context.Context) {
	d.called = true
}

func Test_powerAwarePlugin_Run(t *testing.T) {
	t.Parallel()
	testAdvisor := &dummyAdvisor{}
	type fields struct {
		name    string
		dryRun  bool
		advisor advisor.PowerAwareAdvisor
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
			name: "happy path calls advisor Run",
			fields: fields{
				advisor: testAdvisor,
			},
			args: args{
				ctx: context.TODO(),
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := powerAwarePlugin{
				name:    tt.fields.name,
				dryRun:  tt.fields.dryRun,
				advisor: tt.fields.advisor,
			}
			p.Run(tt.args.ctx)
			if !testAdvisor.called {
				t.Error("expected advisor Run called; but not")
			}
		})
	}
}
