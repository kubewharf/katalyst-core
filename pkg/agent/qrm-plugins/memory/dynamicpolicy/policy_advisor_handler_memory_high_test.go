//go:build linux
// +build linux

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
	"sync"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/opencontainers/runc/libcontainer/cgroups"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/memoryadvisor"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	common "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
)

var memoryHighTestMutex sync.Mutex

func TestDynamicPolicy_handleAdvisorMemoryHigh(t *testing.T) {
	t.Parallel()

	t.Run("invalid memory high value", func(t *testing.T) {
		t.Parallel()
		memoryHighTestMutex.Lock()
		defer memoryHighTestMutex.Unlock()

		p := &DynamicPolicy{}
		calculationInfo := &advisorsvc.CalculationInfo{
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{
					string(memoryadvisor.ControlKnobKeyMemoryHigh): "invalid",
				},
			},
		}

		err := p.handleAdvisorMemoryHigh(nil, nil, nil, metrics.DummyMetrics{}, nil, "", "", calculationInfo, nil)
		assert.Error(t, err)
	})

	t.Run("cgroupv1 mode", func(t *testing.T) {
		t.Parallel()
		memoryHighTestMutex.Lock()
		defer memoryHighTestMutex.Unlock()
		defer mockey.UnPatchAll()

		mockey.Mock(cgroups.IsCgroup2UnifiedMode).IncludeCurrentGoRoutine().To(func() bool {
			return false
		}).Build()

		p := &DynamicPolicy{}
		calculationInfo := &advisorsvc.CalculationInfo{
			CgroupPath: "/kubepods/besteffort",
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{
					string(memoryadvisor.ControlKnobKeyMemoryHigh): "228000000000",
				},
			},
		}

		err := p.handleAdvisorMemoryHigh(nil, nil, nil, metrics.DummyMetrics{}, nil, "", "", calculationInfo, nil)
		assert.NoError(t, err)
	})

	t.Run("empty cgroup path", func(t *testing.T) {
		t.Parallel()
		memoryHighTestMutex.Lock()
		defer memoryHighTestMutex.Unlock()
		defer mockey.UnPatchAll()

		mockey.Mock(cgroups.IsCgroup2UnifiedMode).IncludeCurrentGoRoutine().To(func() bool {
			return true
		}).Build()

		p := &DynamicPolicy{}
		calculationInfo := &advisorsvc.CalculationInfo{
			CgroupPath: "",
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{
					string(memoryadvisor.ControlKnobKeyMemoryHigh): "228000000000",
				},
			},
		}

		err := p.handleAdvisorMemoryHigh(nil, nil, nil, metrics.DummyMetrics{}, nil, "", "", calculationInfo, nil)
		assert.NoError(t, err)
	})

	t.Run("success apply memory high", func(t *testing.T) {
		t.Parallel()
		memoryHighTestMutex.Lock()
		defer memoryHighTestMutex.Unlock()
		defer mockey.UnPatchAll()

		var capturedCgroupPath string
		var capturedMemoryData *common.MemoryData
		applyMemoryCalled := false

		mockey.Mock(cgroups.IsCgroup2UnifiedMode).IncludeCurrentGoRoutine().To(func() bool {
			return true
		}).Build()
		mockey.Mock(cgroupmgr.ApplyMemoryWithRelativePath).IncludeCurrentGoRoutine().To(func(cgroupPath string, memoryData *common.MemoryData) error {
			capturedCgroupPath = cgroupPath
			capturedMemoryData = memoryData
			applyMemoryCalled = true
			return nil
		}).Build()

		p := &DynamicPolicy{}
		calculationInfo := &advisorsvc.CalculationInfo{
			CgroupPath: "/kubepods/besteffort",
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{
					string(memoryadvisor.ControlKnobKeyMemoryHigh): "228000000000",
				},
			},
		}

		err := p.handleAdvisorMemoryHigh(nil, nil, nil, metrics.DummyMetrics{}, nil, "", "", calculationInfo, nil)
		assert.NoError(t, err)
		assert.True(t, applyMemoryCalled)
		assert.Equal(t, "/kubepods/besteffort", capturedCgroupPath)
		assert.Equal(t, int64(228000000000), capturedMemoryData.HighInBytes)
	})
}
