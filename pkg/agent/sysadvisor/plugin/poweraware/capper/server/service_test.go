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

package server

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/metadata"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func Test_powerCapAdvisorPluginServer_Reset(t *testing.T) {
	t.Parallel()

	pcs := newPowerCapService(&metrics.DummyMetrics{})
	pcs.started = true
	pcs.Reset()

	assert.Equal(t,
		&capper.CapInstruction{OpCode: "-1"},
		pcs.capInstruction,
		"the latest power capping instruction should be RESET",
	)
}

func Test_powerCapAdvisorPluginServer_Cap(t *testing.T) {
	t.Parallel()

	pcs := newPowerCapService(&metrics.DummyMetrics{})
	pcs.started = true
	pcs.Cap(context.TODO(), 111, 123)

	assert.Equal(t,
		&capper.CapInstruction{
			OpCode:          "4",
			OpCurrentValue:  "123",
			OpTargetValue:   "111",
			RawCurrentValue: 123,
			RawTargetValue:  111,
		},
		pcs.capInstruction,
		"the latest power capping instruction should be what was just to Cap",
	)
}

func Test_powerCapService_getAdviceWithClientReadySignal(t *testing.T) {
	t.Parallel()
	type fields struct {
		capInstruction *capper.CapInstruction
	}
	type args struct {
		ctx     context.Context
		request *advisorsvc.GetAdviceRequest
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    *advisorsvc.GetAdviceResponse
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "empty response for none instruction",
			fields: fields{
				capInstruction: nil,
			},
			args: args{
				ctx: context.TODO(),
			},
			want: &advisorsvc.GetAdviceResponse{
				PodEntries:   nil,
				ExtraEntries: nil,
			},
			wantErr: assert.NoError,
		},
		{
			name: "happy path of an instruction",
			fields: fields{
				capInstruction: &capper.CapInstruction{
					OpCode:          "4",
					OpCurrentValue:  "100",
					OpTargetValue:   "90",
					RawTargetValue:  90,
					RawCurrentValue: 100,
				},
			},
			args: args{
				ctx: context.TODO(),
			},
			want: &advisorsvc.GetAdviceResponse{
				PodEntries: nil,
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CgroupPath: "",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{
								"op-code":          "4",
								"op-current-value": "100",
								"op-target-value":  "90",
							},
						},
					},
				},
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &powerCapService{
				emitter: metricspool.DummyMetricsEmitterPool{}.GetDefaultMetricsEmitter(),
				longPoller: &longPoller{
					timeout:       time.Second * 1,
					dataUpdatedCh: make(chan struct{}),
				},
			}

			clientReadyCh := make(chan struct{})
			inGetAdvice := atomic.LoadInt32(&p.activeGetAdvices) > 0
			assert.False(t, inGetAdvice, "false as not yet run GetAdvice call")
			go func() {
				<-clientReadyCh
				p.Lock()
				defer p.Unlock()
				inGetAdvice = atomic.LoadInt32(&p.activeGetAdvices) > 0
				p.capInstruction = tt.fields.capInstruction
				p.longPoller.setDataUpdated()
			}()

			t.Log("advisor will set update ready signal when client is ready (already get hold of broadcast chan")
			got, err := p.getAdviceWithClientReadySignal(tt.args.ctx, tt.args.request, clientReadyCh)

			if !tt.wantErr(t, err, fmt.Sprintf("getAdviceWithClientReadySignal(%v, %v)", tt.args.ctx, tt.args.request)) {
				return
			}
			assert.Equalf(t, tt.want, got, "getAdviceWithClientReadySignal(%v, %v)", tt.args.ctx, tt.args.request)
			assert.True(t, inGetAdvice, "should have been in GetAdvice")

			inGetAdvice = atomic.LoadInt32(&p.activeGetAdvices) > 0
			assert.False(t, inGetAdvice, "false as GetAdvice call has done")
		})
	}
}

func Test_powerCapService_GetAdvice_when_it_had_missed_reset(t *testing.T) {
	t.Parallel()

	mdYes := metadata.Pairs("x-apply-previous-reset", "yes")
	mdOther := metadata.Pairs("x-other", "other")

	type args struct {
		ctx     context.Context
		request *advisorsvc.GetAdviceRequest
	}
	tests := []struct {
		name    string
		args    args
		want    *advisorsvc.GetAdviceResponse
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "reset response for yes apply request",
			args: args{
				ctx: metadata.NewOutgoingContext(context.Background(), mdYes),
			},
			want: &advisorsvc.GetAdviceResponse{
				PodEntries: nil,
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CgroupPath: "",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{
								"op-code":          "-1",
								"op-current-value": "",
								"op-target-value":  "",
							},
						},
					},
				},
			},
			wantErr: assert.NoError,
		},
		{
			name: "empty for nil metadata request",
			args: args{
				ctx: metadata.NewOutgoingContext(context.Background(), mdOther),
			},
			want: &advisorsvc.GetAdviceResponse{
				PodEntries:   nil,
				ExtraEntries: nil,
			},
			wantErr: assert.NoError,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// extract out Outgoing Metadata, and assemble a server side Incoming Context
			outgoingMD, ok := metadata.FromOutgoingContext(tt.args.ctx)
			assert.True(t, ok)
			serverCtx := metadata.NewIncomingContext(context.Background(), outgoingMD)

			p := &powerCapService{
				capInstruction: capper.PowerCapReset,
				emitter:        metricspool.DummyMetricsEmitterPool{}.GetDefaultMetricsEmitter(),
				longPoller: &longPoller{
					timeout:       time.Millisecond * 5,
					dataUpdatedCh: make(chan struct{}),
				},
			}

			t.Log("advisor had reset instruction")
			got, err := p.GetAdvice(serverCtx, tt.args.request)

			if !tt.wantErr(t, err, fmt.Sprintf("GetAdvice(%v, %v)", tt.args.ctx, tt.args.request)) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetAdvice(%v, %v)", tt.args.ctx, tt.args.request)
		})
	}
}
