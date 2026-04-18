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
	return args.Get(0).(*plan.MBPlan), args.Error(1)
}

func TestPControllerAdvisor_GetPlan_CapDown(t *testing.T) {
	t.Parallel()

	dummyStats := monitor.DomainStats{
		Outgoings: map[int]monitor.DomainMonStat{
			0: {
				"dedicated": {
					0: {TotalMB: 10000},
					1: {TotalMB: 35000},
				},
			},
		},
	}

	dummyPlan := plan.MBPlan{
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

	mockInner := new(mockAdvisor)
	mockInner.On("GetPlan", context.TODO(), &dummyStats).Return(&dummyPlan, nil)

	pCtrl := pControllerAdvisor{
		ccdMinMB: 2000,
		ccdMaxMB: 60000,
		inner:    mockInner,
		groupStates: map[string]*groupPCtrlState{
			"dedicated": {
				pCtrl: pController{
					kp:     0.1,
					target: 24000,
				},
				ccdCapMB: 45000,
			},
		},
	}

	expectedPlan := &plan.MBPlan{
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
	}

	resultPlan, err := pCtrl.GetPlan(context.TODO(), &dummyStats)
	if err != nil {
		t.Errorf("unexpected error %v", err)
	}

	t.Logf("result plan = %v", resultPlan)
	assert.Equal(t, expectedPlan, resultPlan)
	assert.Equal(t, 45000-1100, pCtrl.groupStates["dedicated"].ccdCapMB, "cap should decrease: 45000 + 0.1*(24000-35000)")
}

func TestPControllerAdvisor_GetPlan_CapUp(t *testing.T) {
	t.Parallel()

	dummyStats := monitor.DomainStats{
		Outgoings: map[int]monitor.DomainMonStat{
			0: {
				"dedicated": {
					0: {TotalMB: 8000},
					1: {TotalMB: 15000},
				},
			},
		},
	}

	dummyPlan := plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"dedicated": {
				0: 9000,
				1: 15500,
			},
		},
	}

	mockInner := new(mockAdvisor)
	mockInner.On("GetPlan", context.TODO(), &dummyStats).Return(&dummyPlan, nil)

	pCtrl := pControllerAdvisor{
		ccdMinMB: 2000,
		ccdMaxMB: 60000,
		inner:    mockInner,
		groupStates: map[string]*groupPCtrlState{
			"dedicated": {
				pCtrl: pController{
					kp:     0.1,
					target: 24000,
				},
				ccdCapMB: 15000,
			},
		},
	}

	expectedPlan := &plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{
			"dedicated": {
				0: 9000,
				1: 15500, // 15500 < new cap 15900, preserved (would have been clamped to 15000 under old cap)
			},
		},
	}

	resultPlan, err := pCtrl.GetPlan(context.TODO(), &dummyStats)
	if err != nil {
		t.Errorf("unexpected error %v", err)
	}

	t.Logf("result plan = %v", resultPlan)
	assert.Equal(t, expectedPlan, resultPlan)
	assert.Equal(t, 15000+900, pCtrl.groupStates["dedicated"].ccdCapMB, "cap should increase: 15000 + 0.1*(24000-15000)")
}
