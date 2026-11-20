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

package dynamicpolicy

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/asyncworker"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestDynamicPolicy_handleAdvisorDyingMemcgReclaim(t *testing.T) {
	t.Parallel()
	t.Run("empty cgroup path", func(t *testing.T) {
		t.Parallel()
		deps := dyingMemcgReclaimDependencies{}
		calculationInfo := &advisorsvc.CalculationInfo{
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{},
			},
		}

		err := handleAdvisorDyingMemcgReclaim(metrics.DummyMetrics{}, "pod", "container", calculationInfo, deps)
		assert.Error(t, err)
	})

	t.Run("disabled dying memcg reclaim", func(t *testing.T) {
		t.Parallel()
		disableSwapCalled := false
		addWorkCalled := false
		getEffectiveCalled := false

		deps := dyingMemcgReclaimDependencies{
			disableSwapMax: func(_ string) error {
				disableSwapCalled = true
				return nil
			},
			addWork: func(_ *asyncworker.Work, _ asyncworker.DuplicateWorkPolicy) error {
				addWorkCalled = true
				return nil
			},
			getEffective: func(_ string) (machine.CPUSet, machine.CPUSet, error) {
				getEffectiveCalled = true
				return machine.NewCPUSet(0), machine.NewCPUSet(0), nil
			},
		}

		calculationInfo := &advisorsvc.CalculationInfo{
			CgroupPath: "/sys/fs/cgroup/kubepods/burstable",
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{
					string(memoryadvisor.ControlKnobKeySwapMax):           consts.ControlKnobOFF,
					string(memoryadvisor.ControlKnowKeyDyingMemcgReclaim): consts.ControlKnobOFF,
				},
			},
		}

		err := handleAdvisorDyingMemcgReclaim(metrics.DummyMetrics{}, "pod", "container", calculationInfo, deps)
		assert.NoError(t, err)
		assert.True(t, disableSwapCalled)
		assert.False(t, addWorkCalled)
		assert.False(t, getEffectiveCalled)
	})

	t.Run("enabled dying memcg reclaim", func(t *testing.T) {
		t.Parallel()
		setSwapCalled := false
		var capturedWork *asyncworker.Work
		var capturedPolicy asyncworker.DuplicateWorkPolicy
		mems := machine.NewCPUSet(0, 1)

		deps := dyingMemcgReclaimDependencies{
			setSwapMax: func(_ string) error {
				setSwapCalled = true
				return nil
			},
			getEffective: func(_ string) (machine.CPUSet, machine.CPUSet, error) {
				return machine.NewCPUSet(0), mems, nil
			},
			addWork: func(work *asyncworker.Work, policy asyncworker.DuplicateWorkPolicy) error {
				capturedWork = work
				capturedPolicy = policy
				return nil
			},
		}

		calculationInfo := &advisorsvc.CalculationInfo{
			CgroupPath: "/sys/fs/cgroup/kubepods/burstable",
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{
					string(memoryadvisor.ControlKnobKeySwapMax):           consts.ControlKnobON,
					string(memoryadvisor.ControlKnowKeyDyingMemcgReclaim): consts.ControlKnobON,
				},
			},
		}

		emitter := metrics.DummyMetrics{}
		err := handleAdvisorDyingMemcgReclaim(emitter, "pod", "container", calculationInfo, deps)
		assert.NoError(t, err)
		assert.True(t, setSwapCalled)
		assert.NotNil(t, capturedWork)

		expectedAbsPath := "/sys/fs/cgroup/kubepods/burstable"
		expectedWorkName := util.GetCgroupAsyncWorkName("/sys/fs/cgroup/kubepods/burstable", memoryPluginAsyncWorkTopicDyingMemcgReclaim)
		assert.Equal(t, expectedWorkName, capturedWork.Name)
		assert.Equal(t, asyncworker.DuplicateWorkPolicy(asyncworker.DuplicateWorkPolicyOverride), capturedPolicy)
		assert.Equal(t, expectedAbsPath, capturedWork.Params[0])
		assert.Equal(t, emitter, capturedWork.Params[1])
		assert.Equal(t, "pod", capturedWork.Params[2])
		assert.Equal(t, "container", capturedWork.Params[3])
		assert.Equal(t, mems, capturedWork.Params[4])
		assert.Equal(t, reflect.ValueOf(cgroupmgr.DyingMemcgReclaimWithAbsolutePath).Pointer(), reflect.ValueOf(capturedWork.Fn).Pointer())
	})
}
