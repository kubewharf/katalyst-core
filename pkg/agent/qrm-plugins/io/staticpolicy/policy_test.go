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

package staticpolicy

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metaserveragent "github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func makeMetaServer() *metaserver.MetaServer {
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 2, 4)

	return &metaserver.MetaServer{
		MetaAgent: &metaserveragent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology:      cpuTopology,
				ExtraNetworkInfo: &machine.ExtraNetworkInfo{},
			},
		},
		ExternalManager: external.InitExternalManager(&pod.PodFetcherStub{}),
	}
}

func makeTestGenericContext(t *testing.T) *agent.GenericContext {
	genericCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{})
	assert.NoError(t, err)

	return &agent.GenericContext{
		GenericContext: genericCtx,
		MetaServer:     makeMetaServer(),
		PluginManager:  nil,
	}
}

func TestNewStaticPolicy(t *testing.T) {
	t.Parallel()

	agentCtx := makeTestGenericContext(t)
	conf := generateTestConfiguration(t)

	type args struct {
		agentCtx  *agent.GenericContext
		conf      *config.Configuration
		in2       interface{}
		agentName string
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		want1   agent.Component
		wantErr bool
	}{
		{
			name: "new static policy",
			args: args{
				agentCtx:  agentCtx,
				conf:      conf,
				agentName: qrm.QRMPluginNameIO,
			},
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, got1, err := NewStaticPolicy(tt.args.agentCtx, tt.args.conf, tt.args.in2, tt.args.agentName)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewStaticPolicy() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if got != tt.want {
				t.Errorf("NewStaticPolicy() got = %v, want %v", got, tt.want)
			}

			assert.NotNilf(t, got1, "got nil policy object")
		})
	}
}

func TestStaticPolicy_Start(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "start static policy",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    false,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			if err := p.Start(); (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.Start() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStaticPolicy_Stop(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr bool
	}{
		{
			name: "stop static policy",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			if err := p.Stop(); (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.Stop() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestStaticPolicy_Name(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "get policy name",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			want: fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			if got := p.Name(); got != tt.want {
				t.Errorf("StaticPolicy.Name() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStaticPolicy_ResourceName(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "get policy name",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			want: "",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			if got := p.ResourceName(); got != tt.want {
				t.Errorf("StaticPolicy.ResourceName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStaticPolicy_GetTopologyHints(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		req *pluginapi.ResourceRequest
	}
	tests := []struct {
		name     string
		fields   fields
		args     args
		wantResp *pluginapi.ResourceHintsResponse
		wantErr  bool
	}{
		{
			name: "get topology hints",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				req: &pluginapi.ResourceRequest{},
			},
			wantResp: &pluginapi.ResourceHintsResponse{},
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			gotResp, err := p.GetTopologyHints(tt.args.in0, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.GetTopologyHints() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(gotResp, tt.wantResp) {
				t.Errorf("StaticPolicy.GetTopologyHints() = %v, want %v", gotResp, tt.wantResp)
			}
		})
	}
}

func TestStaticPolicy_GetPodTopologyHints(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		req *pluginapi.PodResourceRequest
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name: "get pod topology hints",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				req: &pluginapi.PodResourceRequest{},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			_, err := p.GetPodTopologyHints(tt.args.in0, tt.args.req)
			assert.NotNil(t, err)
		})
	}
}

func TestStaticPolicy_RemovePod(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		req *pluginapi.RemovePodRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *pluginapi.RemovePodResponse
		wantErr bool
	}{
		{
			name: "remove pod",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				req: &pluginapi.RemovePodRequest{},
			},
			want:    &pluginapi.RemovePodResponse{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			got, err := p.RemovePod(tt.args.in0, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.RemovePod() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("StaticPolicy.RemovePod() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStaticPolicy_GetResourcesAllocation(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		in1 *pluginapi.GetResourcesAllocationRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *pluginapi.GetResourcesAllocationResponse
		wantErr bool
	}{
		{
			name: "get resources allocation",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				in1: &pluginapi.GetResourcesAllocationRequest{},
			},
			want:    &pluginapi.GetResourcesAllocationResponse{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			got, err := p.GetResourcesAllocation(tt.args.in0, tt.args.in1)
			if (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.GetResourcesAllocation() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("StaticPolicy.GetResourcesAllocation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStaticPolicy_GetTopologyAwareResources(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		in1 *pluginapi.GetTopologyAwareResourcesRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *pluginapi.GetTopologyAwareResourcesResponse
		wantErr bool
	}{
		{
			name: "get topology-aware resources",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				in1: &pluginapi.GetTopologyAwareResourcesRequest{},
			},
			want:    &pluginapi.GetTopologyAwareResourcesResponse{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			got, err := p.GetTopologyAwareResources(tt.args.in0, tt.args.in1)
			if (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.GetTopologyAwareResources() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("StaticPolicy.GetTopologyAwareResources() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStaticPolicy_GetTopologyAwareAllocatableResources(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		in1 *pluginapi.GetTopologyAwareAllocatableResourcesRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *pluginapi.GetTopologyAwareAllocatableResourcesResponse
		wantErr bool
	}{
		{
			name: "get topology-aware allocatable resources",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				in1: &pluginapi.GetTopologyAwareAllocatableResourcesRequest{},
			},
			want:    &pluginapi.GetTopologyAwareAllocatableResourcesResponse{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			got, err := p.GetTopologyAwareAllocatableResources(tt.args.in0, tt.args.in1)
			if (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.GetTopologyAwareAllocatableResources() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("StaticPolicy.GetTopologyAwareAllocatableResources() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStaticPolicy_GetResourcePluginOptions(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		in1 *pluginapi.Empty
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *pluginapi.ResourcePluginOptions
		wantErr bool
	}{
		{
			name: "test get resource plugin options",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				in1: &pluginapi.Empty{},
			},
			want: &pluginapi.ResourcePluginOptions{
				PreStartRequired:      false,
				WithTopologyAlignment: false,
				NeedReconcile:         false,
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			got, err := p.GetResourcePluginOptions(tt.args.in0, tt.args.in1)
			if (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.GetResourcePluginOptions() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("StaticPolicy.GetResourcePluginOptions() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStaticPolicy_Allocate(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		req *pluginapi.ResourceRequest
	}
	tests := []struct {
		name     string
		fields   fields
		args     args
		wantResp *pluginapi.ResourceAllocationResponse
		wantErr  bool
	}{
		{
			name: "test allocate",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				req: &pluginapi.ResourceRequest{
					PodNamespace:  "test",
					PodName:       "test",
					ContainerName: "test",
				},
			},
			wantResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:  "test",
				PodName:       "test",
				ContainerName: "test",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			gotResp, err := p.Allocate(tt.args.in0, tt.args.req)
			if (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.Allocate() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(gotResp, tt.wantResp) {
				t.Errorf("StaticPolicy.Allocate() = %v, want %v", gotResp, tt.wantResp)
			}
		})
	}
}

func TestStaticPolicy_AllocateForPod(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		req *pluginapi.PodResourceRequest
	}
	tests := []struct {
		name     string
		fields   fields
		args     args
		wantResp *pluginapi.ResourceAllocationResponse
		wantErr  bool
	}{
		{
			name: "test allocateForPod",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				req: &pluginapi.PodResourceRequest{
					PodNamespace: "test",
					PodName:      "test",
				},
			},
			wantResp: &pluginapi.ResourceAllocationResponse{
				PodNamespace:  "test",
				PodName:       "test",
				ContainerName: "test",
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			_, err := p.AllocateForPod(tt.args.in0, tt.args.req)
			assert.NotNil(t, err)
		})
	}
}

func TestStaticPolicy_PreStartContainer(t *testing.T) {
	t.Parallel()

	type fields struct {
		name       string
		stopCh     chan struct{}
		started    bool
		emitter    metrics.MetricEmitter
		metaServer *metaserver.MetaServer
		agentCtx   *agent.GenericContext
	}
	type args struct {
		in0 context.Context
		in1 *pluginapi.PreStartContainerRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *pluginapi.PreStartContainerResponse
		wantErr bool
	}{
		{
			name: "test pre-start container",
			fields: fields{
				name:       fmt.Sprintf("%s_%s", qrm.QRMPluginNameIO, IOResourcePluginPolicyNameStatic),
				stopCh:     make(chan struct{}),
				started:    true,
				emitter:    metrics.DummyMetrics{},
				metaServer: makeMetaServer(),
				agentCtx:   makeTestGenericContext(t),
			},
			args: args{
				in0: context.Background(),
				in1: &pluginapi.PreStartContainerRequest{},
			},
			want:    &pluginapi.PreStartContainerResponse{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := &StaticPolicy{
				name:       tt.fields.name,
				stopCh:     tt.fields.stopCh,
				started:    tt.fields.started,
				emitter:    tt.fields.emitter,
				metaServer: tt.fields.metaServer,
				agentCtx:   tt.fields.agentCtx,
			}
			got, err := p.PreStartContainer(tt.args.in0, tt.args.in1)
			if (err != nil) != tt.wantErr {
				t.Errorf("StaticPolicy.PreStartContainer() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("StaticPolicy.PreStartContainer() = %v, want %v", got, tt.want)
			}
		})
	}
}
