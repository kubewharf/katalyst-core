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

package advisor

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
)

type mockAdvisor struct {
	mock.Mock
}

func (m *mockAdvisor) GetPlan(ctx context.Context, domainsMon *monitor.DomainStats) (*plan.MBPlan, error) {
	args := m.Called(ctx, domainsMon)

	var returnedPlan *plan.MBPlan
	if v := args.Get(0); v != nil {
		returnedPlan = v.(*plan.MBPlan)
	}

	return returnedPlan, args.Error(1)
}

func TestPControllerAdvisor_GetPlan_May_Update_CCDCap(t *testing.T) {
	t.Parallel()

	// test data for case cap-down
	dummyStatsCapDown := monitor.DomainStats{
		Outgoings: map[int]monitor.DomainMonStat{
			0: {
				"dedicated": {
					0: {TotalMB: 10000},
					1: {TotalMB: 35000},
				},
			},
		},
	}
	dummyPlanCapDown := plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"system": {
				0: 123,
				1: 456,
			},
			"dedicated": {
				0: 16000,
				1: 45000,
			},
		},
	}
	mockInnerCapDown := new(mockAdvisor)
	mockInnerCapDown.On("GetPlan", context.TODO(), &dummyStatsCapDown).Return(
		&dummyPlanCapDown, nil)

	// test data for case cap-up
	dummyStatsCapUp := monitor.DomainStats{
		Outgoings: map[int]monitor.DomainMonStat{
			0: {
				"dedicated": {
					0: {TotalMB: 8000},
					1: {TotalMB: 15000},
				},
			},
		},
	}
	dummyPlanCapUp := plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"dedicated": {
				0: 9000,
				1: 15500,
			},
		},
	}
	mockInnerCapUp := new(mockAdvisor)
	mockInnerCapUp.On("GetPlan", context.TODO(), &dummyStatsCapUp).Return(
		&dummyPlanCapUp, nil)

	// test data for case asymmetric Kq
	dummyPlanCapUpAsym := plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"dedicated": {
				0: 9000,
				1: 15500,
			},
		},
	}
	mockInnerCapUpAsym := new(mockAdvisor)
	mockInnerCapUpAsym.On("GetPlan", context.TODO(), &dummyStatsCapUp).Return(
		&dummyPlanCapUpAsym, nil)

	// test data for negative case cap-no-data-error
	dummyStatsCapNoData := monitor.DomainStats{}
	mockInnerCapNoData := new(mockAdvisor)
	mockInnerCapNoData.On("GetPlan", context.TODO(), &dummyStatsCapNoData).Return(
		nil, fmt.Errorf("no data error"))

	// test data for case cap-no-usage
	dummyStatsCapNoUsage := monitor.DomainStats{}
	dummyPlanCapNoUsage := plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"system": {
				0: 123,
				1: 456,
			},
		},
	}
	mockInnerCapNoUsage := new(mockAdvisor)
	mockInnerCapNoUsage.On("GetPlan", context.TODO(), &dummyStatsCapNoUsage).Return(
		&dummyPlanCapNoUsage, nil)

	type fields struct {
		ccdMinMB    int
		ccdMaxMB    int
		inner       Advisor
		groupStates map[string]*groupPCtrlState
	}

	tests := []struct {
		name                  string
		fields                fields
		domainsMon            *monitor.DomainStats
		wantErr               bool
		wantPlan              *plan.MBPlan
		wantDedicatedCCDCapMB int
	}{
		{
			name: "mb stat higher over the target leads to lower ccd cap",
			fields: fields{
				ccdMinMB: 2000,
				ccdMaxMB: 60000,
				inner:    mockInnerCapDown,
				groupStates: map[string]*groupPCtrlState{
					"dedicated": {
						pCtrl: pController{
							kp:     0.1,
							kq:     0.1,
							target: 24000,
						},
						ccdCapMB: 45000, // current dedicated group ccd cap
					},
				},
			},
			domainsMon: &dummyStatsCapDown,
			wantErr:    false,
			wantPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"system": {
						0: 123,
						1: 456,
					},
					"dedicated": {
						0: 16000,
						1: 45000 - 1100, // 0.1 * (35000 - 24000)
					},
				},
			},
			wantDedicatedCCDCapMB: 45000 - 1100, // expected dedicated group ccd cap: 45000 + 0.1*(24000-35000)
		},
		{
			name: "mb stat lower much under the target leads to raised ccd cap",
			fields: fields{
				ccdMinMB: 2000,
				ccdMaxMB: 60000,
				inner:    mockInnerCapUp,
				groupStates: map[string]*groupPCtrlState{
					"dedicated": {
						pCtrl: pController{
							kp:     0.1,
							kq:     0.1,
							target: 24000,
						},
						ccdCapMB: 15000,
					},
				},
			},
			domainsMon: &dummyStatsCapUp,
			wantErr:    false,
			wantPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"dedicated": {
						0: 9000,
						1: 15500,
					},
				},
			},
			wantDedicatedCCDCapMB: 15000 + 900,
		},
		{
			name: "mb stat higher over the target with asymmetric Kq leads to smaller cap raise",
			fields: fields{
				ccdMinMB: 2000,
				ccdMaxMB: 60000,
				inner:    mockInnerCapUpAsym,
				groupStates: map[string]*groupPCtrlState{
					"dedicated": {
						pCtrl: pController{
							kp:     0.1,
							kq:     0.05,
							target: 24000,
						},
						ccdCapMB: 15000,
					},
				},
			},
			domainsMon: &dummyStatsCapUp,
			wantErr:    false,
			wantPlan: &plan.MBPlan{
				MBGroups: map[string]plan.GroupCCDPlan{
					"dedicated": {
						0: 9000,
						1: 15450, // 15500 > new cap 15450, capped slightly (would have been clamped to 15000 under old cap)
					},
				},
			},
			wantDedicatedCCDCapMB: 15000 + 450,
		},
		{
			name: "error from inner passed through",
			fields: fields{
				ccdMinMB: 2000,
				ccdMaxMB: 60000,
				inner:    mockInnerCapNoData,
				groupStates: map[string]*groupPCtrlState{
					"dedicated": {
						pCtrl: pController{
							kp:     0.1,
							kq:     0.1,
							target: 24000,
						},
						ccdCapMB: 12345,
					},
				},
			},
			domainsMon:            &dummyStatsCapNoData,
			wantErr:               true,
			wantPlan:              nil,
			wantDedicatedCCDCapMB: 12345,
		},
		{
			name: "no mb usage, no change",
			fields: fields{
				ccdMinMB: 2000,
				ccdMaxMB: 60000,
				inner:    mockInnerCapNoUsage,
				groupStates: map[string]*groupPCtrlState{
					"dedicated": {
						pCtrl: pController{
							kp:     0.1,
							kq:     0.1,
							target: 24000,
						},
						ccdCapMB: 21312,
					},
				},
			},
			domainsMon:            &dummyStatsCapNoUsage,
			wantErr:               false,
			wantPlan:              &dummyPlanCapNoUsage,
			wantDedicatedCCDCapMB: 21312,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pCtrl := pControllerAdvisor{
				ccdMinMB:    tt.fields.ccdMinMB,
				ccdMaxMB:    tt.fields.ccdMaxMB,
				inner:       tt.fields.inner,
				groupStates: tt.fields.groupStates,
			}

			gotPlan, err := pCtrl.GetPlan(context.TODO(), tt.domainsMon)

			mock.AssertExpectationsForObjects(t, pCtrl.inner)

			if (err != nil) != tt.wantErr {
				t.Errorf("pControllerAdvisor.GetPlan() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			t.Logf("result plan = %v", gotPlan)
			assert.Equal(t, tt.wantPlan, gotPlan)
			assert.Equal(t, tt.wantDedicatedCCDCapMB, pCtrl.groupStates["dedicated"].ccdCapMB, "new cap should be")
		})
	}
}
