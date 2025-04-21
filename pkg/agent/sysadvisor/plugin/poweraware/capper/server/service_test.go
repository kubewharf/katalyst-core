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
	"testing"

	"github.com/stretchr/testify/assert"

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

func Test_powerCapService_GetAdvice(t *testing.T) {
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
			args: args{},
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
			args: args{},
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
				capInstruction: tt.fields.capInstruction,
				emitter:        metricspool.DummyMetricsEmitterPool{}.GetDefaultMetricsEmitter(),
			}
			got, err := p.GetAdvice(tt.args.ctx, tt.args.request)
			if !tt.wantErr(t, err, fmt.Sprintf("GetAdvice(%v, %v)", tt.args.ctx, tt.args.request)) {
				return
			}
			assert.Equalf(t, tt.want, got, "GetAdvice(%v, %v)", tt.args.ctx, tt.args.request)
		})
	}
}
