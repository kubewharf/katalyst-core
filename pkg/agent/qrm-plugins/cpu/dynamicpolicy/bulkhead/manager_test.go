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
	"testing"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	bypassutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

type fakePlugin struct {
	name          string
	adjustViews   []*bulkheadutils.CPUSetPartitionView
	periodicCalls int
	enabled       bool
	adjustErr     error
	periodicErr   error
}

func (p *fakePlugin) Name() string { return p.name }

func (p *fakePlugin) Enable(_ *dynamicconfig.Configuration) bool { return p.enabled }

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

func TestRunCPUSetAdjustmentHandlersSkipsUnchangedView(t *testing.T) {
	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), bypassutil.BypassCPUSetAdjustmentHandlerCtx{}); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), bypassutil.BypassCPUSetAdjustmentHandlerCtx{}); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 1 {
		t.Fatalf("expected unchanged view to skip plugin, got %d calls", got)
	}
}

func TestRunCPUSetAdjustmentHandlersDoesNotCacheFailedView(t *testing.T) {
	plugin := &fakePlugin{name: "fake", enabled: true, adjustErr: errors.New("boom")}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), bypassutil.BypassCPUSetAdjustmentHandlerCtx{}); err == nil {
		t.Fatal("expected first run to fail")
	}
	plugin.adjustErr = nil
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), bypassutil.BypassCPUSetAdjustmentHandlerCtx{}); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 2 {
		t.Fatalf("expected failed view not cached, got %d calls", got)
	}
}

func TestRunPeriodicalHandlersContinuesAfterErrors(t *testing.T) {
	pluginA := &fakePlugin{name: "a", periodicErr: errors.New("a failed")}
	pluginB := &fakePlugin{name: "b"}
	m := &Manager{plugins: []bulkheadapi.Plugin{pluginA, pluginB}}

	m.RunPeriodicalHandlers(nil, nil, nil, nil, nil)
	if pluginA.periodicCalls != 1 || pluginB.periodicCalls != 1 {
		t.Fatalf("expected both plugins to run, got a=%d b=%d", pluginA.periodicCalls, pluginB.periodicCalls)
	}
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
