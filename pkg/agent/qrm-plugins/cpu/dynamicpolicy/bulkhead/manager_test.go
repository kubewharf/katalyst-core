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

package bulkhead

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

type fakePlugin struct {
	name          string
	adjustViews   []*bulkheadutils.CPUSetPartitionView
	periodicCalls int
	disabledCalls int
	enableStates  []interface{}
	enabled       bool
	adjustErr     error
	periodicErr   error
	disabledErr   error
}

func (p *fakePlugin) Name() string { return p.name }

func (p *fakePlugin) Enable(in bulkheadapi.HandlerContext) bool {
	p.enableStates = append(p.enableStates, in.State)
	return p.enabled
}

func (p *fakePlugin) CPUSetAdjustmentHandler(_ context.Context, in bulkheadapi.HandlerContext) error {
	p.adjustViews = append(p.adjustViews, in.View)
	return p.adjustErr
}

func (p *fakePlugin) PeriodicalHandler(
	_ context.Context,
	_ bulkheadapi.PeriodicalHandlerContext,
) error {
	p.periodicCalls++
	return p.periodicErr
}

func (p *fakePlugin) CPUSetAdjustmentDisabledHandler(_ context.Context, _ bulkheadapi.HandlerContext) error {
	p.disabledCalls++
	return p.disabledErr
}

func TestRunCPUSetAdjustmentHandlersSkipsUnchangedView(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 1 {
		t.Fatalf("expected unchanged view to skip plugin, got %d calls", got)
	}
}

func TestRunCPUSetAdjustmentHandlersPassesHandlerContextToEnable(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}
	in := cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       cpustate.NewCPUPluginState(nil),
	}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), in); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if len(plugin.enableStates) != 1 {
		t.Fatalf("Enable calls = %d, want 1", len(plugin.enableStates))
	}
	if plugin.enableStates[0] != in.State {
		t.Fatalf("Enable did not receive handler context state")
	}
}

func TestRunCPUSetAdjustmentHandlersReturnsEarlyWhenBulkheadDisabled(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{
		plugins:                     []bulkheadapi.Plugin{plugin},
		lastCPUSetAdjustmentView:    &bulkheadutils.CPUSetPartitionView{},
		lastCPUSetAdjustmentEnabled: map[string]bool{plugin.Name(): true},
	}

	err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(false),
		State:       cpustate.NewCPUPluginState(nil),
	})
	if err != nil {
		t.Fatalf("RunCPUSetAdjustmentHandlers failed: %v", err)
	}
	if len(plugin.enableStates) != 0 {
		t.Fatalf("plugin Enable calls = %d, want 0", len(plugin.enableStates))
	}
	if len(plugin.adjustViews) != 0 {
		t.Fatalf("adjust calls = %d, want 0", len(plugin.adjustViews))
	}
	if plugin.disabledCalls != 0 {
		t.Fatalf("disabled calls = %d, want 0", plugin.disabledCalls)
	}
	if m.lastCPUSetAdjustmentView != nil {
		t.Fatalf("lastCPUSetAdjustmentView should be cleared")
	}
	if m.lastCPUSetAdjustmentEnabled != nil {
		t.Fatalf("lastCPUSetAdjustmentEnabled should be cleared")
	}
}

func TestRunCPUSetAdjustmentHandlersReconcilesAfterBulkheadReenabledWithSameView(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("first enabled run failed: %v", err)
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(false),
	}); err != nil {
		t.Fatalf("disabled bulkhead run failed: %v", err)
	}
	if plugin.disabledCalls != 0 {
		t.Fatalf("disabled calls = %d, want 0", plugin.disabledCalls)
	}
	if len(plugin.enableStates) != 1 {
		t.Fatalf("plugin Enable calls = %d, want 1", len(plugin.enableStates))
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("second enabled run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 2 {
		t.Fatalf("adjust calls = %d, want 2", got)
	}
}

func TestRunCPUSetAdjustmentHandlersCallsDisabledTransitionWhenPluginDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		pluginEnabled      bool
		preRunEnabledState bool
		wantAdjustCalls    int
		wantDisabledCalls  int
	}{
		{
			name:            "plugin enabled",
			pluginEnabled:   true,
			wantAdjustCalls: 1,
		},
		{
			name: "plugin disabled without previous enabled state",
		},
		{
			name:               "bulkhead disabled after previous enabled",
			preRunEnabledState: true,
			wantDisabledCalls:  1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plugin := &fakePlugin{name: "fake", enabled: tt.pluginEnabled}
			m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}
			if tt.preRunEnabledState {
				m.lastCPUSetAdjustmentEnabled = map[string]bool{plugin.Name(): true}
			}

			err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
				DynamicConf: dynamicBulkheadConf(true),
			})
			if err != nil {
				t.Fatalf("RunCPUSetAdjustmentHandlers failed: %v", err)
			}
			if got := len(plugin.adjustViews); got != tt.wantAdjustCalls {
				t.Fatalf("adjust calls = %d, want %d", got, tt.wantAdjustCalls)
			}
			if plugin.disabledCalls != tt.wantDisabledCalls {
				t.Fatalf("disabled calls = %d, want %d", plugin.disabledCalls, tt.wantDisabledCalls)
			}
		})
	}
}

func TestRunCPUSetAdjustmentHandlersDoesNotCacheFailedView(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true, adjustErr: errors.New("boom")}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err == nil {
		t.Fatal("expected first run to fail")
	}
	plugin.adjustErr = nil
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 2 {
		t.Fatalf("expected failed view not cached, got %d calls", got)
	}
}

func TestRunCPUSetAdjustmentHandlersDisabledTransitionInvalidatesCache(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("first enabled run failed: %v", err)
	}

	plugin.enabled = false
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("disabled transition failed: %v", err)
	}

	plugin.enabled = true
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("second enabled run failed: %v", err)
	}

	if got := len(plugin.adjustViews); got != 2 {
		t.Fatalf("expected second enabled run not to be skipped after disabled transition, got %d calls", got)
	}
}

func TestRunCPUSetAdjustmentHandlersCallsDisabledTransitionOnceForPluginDisable(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("enabled run failed: %v", err)
	}

	plugin.enabled = false
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("disabled transition failed: %v", err)
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("stable disabled run failed: %v", err)
	}

	if plugin.disabledCalls != 1 {
		t.Fatalf("disabled transition calls = %d, want 1", plugin.disabledCalls)
	}
}

func TestRunCPUSetAdjustmentHandlersReturnsDisabledHandlerError(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("enabled run failed: %v", err)
	}

	plugin.enabled = false
	plugin.disabledErr = errors.New("disabled failed")
	err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx())
	if err == nil {
		t.Fatalf("expected disabled transition error")
	}
	if got := err.Error(); !strings.Contains(got, "disabled transition failed") {
		t.Fatalf("error = %q, want disabled transition failed", got)
	}
}

func TestRunPeriodicalHandlersContinuesAfterErrors(t *testing.T) {
	t.Parallel()

	pluginA := &fakePlugin{name: "a", periodicErr: errors.New("a failed")}
	pluginB := &fakePlugin{name: "b"}
	m := &Manager{plugins: []bulkheadapi.Plugin{pluginA, pluginB}}

	m.RunPeriodicalHandlers(nil, nil, enabledDynamicAgentConf(), nil, nil)
	if pluginA.periodicCalls != 1 || pluginB.periodicCalls != 1 {
		t.Fatalf("expected both plugins to run, got a=%d b=%d", pluginA.periodicCalls, pluginB.periodicCalls)
	}
}

func TestRunPeriodicalHandlersSkipsPluginsWhenBulkheadDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		bulkheadEnabled bool
		wantCalls       int
	}{
		{
			name: "bulkhead disabled",
		},
		{
			name:            "bulkhead enabled",
			bulkheadEnabled: true,
			wantCalls:       1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plugin := &fakePlugin{name: "fake"}
			m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}
			dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
			dynamicConf.SetDynamicConfiguration(dynamicBulkheadConf(tt.bulkheadEnabled))

			m.RunPeriodicalHandlers(nil, nil, dynamicConf, nil, nil)
			if plugin.periodicCalls != tt.wantCalls {
				t.Fatalf("periodic calls = %d, want %d", plugin.periodicCalls, tt.wantCalls)
			}
		})
	}
}

func dynamicBulkheadConf(enabled bool) *dynamicconfig.Configuration {
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = enabled
	return conf
}

func enabledCPUSetAdjustmentCtx() cpusetutil.CPUSetAdjustmentHandlerCtx {
	return cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
	}
}

func enabledDynamicAgentConf() *dynamicconfig.DynamicAgentConfiguration {
	conf := dynamicconfig.NewDynamicAgentConfiguration()
	conf.SetDynamicConfiguration(dynamicBulkheadConf(true))
	return conf
}

func TestNewManagerRegistersDefaultPluginsInOrder(t *testing.T) {
	t.Parallel()

	m, err := NewManager(nil)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	got := make([]string, 0, len(m.plugins))
	for _, plugin := range m.plugins {
		got = append(got, plugin.Name())
	}
	want := []string{"cpuset_topology", "workqueue"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected plugin names, got %v want %v", got, want)
	}
}
