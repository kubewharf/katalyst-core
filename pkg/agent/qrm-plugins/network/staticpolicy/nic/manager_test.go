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

package nic

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/nic/checker"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type MockNICHealthChecker struct {
	mock.Mock
}

func (m *MockNICHealthChecker) CheckHealth(nic machine.InterfaceInfo) (bool, error) {
	args := m.Called(nic)
	return args.Bool(0), args.Error(1)
}

func TestNewNICManager(t *testing.T) {
	t.Parallel()
	mockMetaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				ExtraNetworkInfo: &machine.ExtraNetworkInfo{},
			},
		},
	}
	mockEmitter := &metrics.DummyMetrics{}
	mockConf, err := options.NewOptions().Config()
	assert.NoError(t, err)

	manager, err := NewNICManager(mockMetaServer, mockEmitter, mockConf)
	assert.NoError(t, err)
	assert.NotNil(t, manager)
}

func TestGetNICs(t *testing.T) {
	t.Parallel()

	t.Run("Single NIC", func(t *testing.T) {
		t.Parallel()
		manager := &nicManagerImpl{
			nics: &NICs{
				HealthyNICs: []machine.InterfaceInfo{{Iface: "eth0"}},
			},
		}

		nics := manager.GetNICs()
		assert.Len(t, nics.HealthyNICs, 1)
		assert.Equal(t, "eth0", nics.HealthyNICs[0].Iface)
	})

	t.Run("Empty NICs", func(t *testing.T) {
		t.Parallel()
		manager := &nicManagerImpl{
			nics: &NICs{},
		}

		nics := manager.GetNICs()
		assert.Empty(t, nics.HealthyNICs)
	})

	t.Run("Multiple NICs", func(t *testing.T) {
		t.Parallel()
		manager := &nicManagerImpl{
			nics: &NICs{
				HealthyNICs: []machine.InterfaceInfo{
					{Iface: "eth0"},
					{Iface: "eth1"},
				},
			},
		}

		nics := manager.GetNICs()
		assert.Len(t, nics.HealthyNICs, 2)
		assert.ElementsMatch(t, []string{"eth0", "eth1"}, []string{nics.HealthyNICs[0].Iface, nics.HealthyNICs[1].Iface})
	})
}

func TestUpdateNICs(t *testing.T) {
	t.Parallel()

	t.Run("Update with valid NICs", func(t *testing.T) {
		t.Parallel()
		mockChecker := new(MockNICHealthChecker)
		mockChecker.On("CheckHealth", mock.Anything).Return(true, nil)

		manager := &nicManagerImpl{
			nics: &NICs{},
			defaultAllocatableNICs: []machine.InterfaceInfo{
				{Iface: "eth0"},
			},
			checkers: map[string]checker.NICHealthChecker{
				"mockChecker": mockChecker,
			},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		manager.updateNICs(ctx)

		nics := manager.GetNICs()
		assert.Len(t, nics.HealthyNICs, 1)
		assert.Equal(t, "eth0", nics.HealthyNICs[0].Iface)
	})

	t.Run("No NICs available", func(t *testing.T) {
		t.Parallel()
		manager := &nicManagerImpl{
			nics:                   &NICs{},
			defaultAllocatableNICs: []machine.InterfaceInfo{},
			checkers:               map[string]checker.NICHealthChecker{},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		manager.updateNICs(ctx)

		nics := manager.GetNICs()
		assert.Empty(t, nics.HealthyNICs)
		assert.Empty(t, nics.UnhealthyNICs)
	})

	t.Run("No health checkers", func(t *testing.T) {
		t.Parallel()
		manager := &nicManagerImpl{
			nics: &NICs{},
			defaultAllocatableNICs: []machine.InterfaceInfo{
				{Iface: "eth0"},
			},
			checkers: map[string]checker.NICHealthChecker{},
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		manager.updateNICs(ctx)

		nics := manager.GetNICs()
		assert.Empty(t, nics.HealthyNICs)
		assert.Empty(t, nics.UnhealthyNICs)
	})
}

func TestCheckNICs(t *testing.T) {
	t.Parallel()
	mockEmitter := &metrics.DummyMetrics{}

	t.Run("All NICs healthy", func(t *testing.T) {
		t.Parallel()
		mockChecker := new(MockNICHealthChecker)
		mockChecker.On("CheckHealth", mock.Anything).Return(true, nil)

		checkers := map[string]checker.NICHealthChecker{"mockChecker": mockChecker}
		nics := []machine.InterfaceInfo{{Iface: "eth0"}, {Iface: "eth1"}}
		n := &nicManagerImpl{
			checkers: checkers,
			emitter:  mockEmitter,
		}

		result, err := n.checkNICs(nics)
		assert.NoError(t, err)
		assert.Len(t, result.HealthyNICs, 2)
		assert.Empty(t, result.UnhealthyNICs)
	})

	t.Run("Some NICs unhealthy", func(t *testing.T) {
		t.Parallel()
		mockChecker := new(MockNICHealthChecker)
		mockChecker.On("CheckHealth", mock.Anything).Return(false, nil).Once()
		mockChecker.On("CheckHealth", mock.Anything).Return(true, nil).Once()

		checkers := map[string]checker.NICHealthChecker{"mockChecker": mockChecker}
		nics := []machine.InterfaceInfo{{Iface: "eth0"}, {Iface: "eth1"}}
		n := &nicManagerImpl{
			checkers: checkers,
			emitter:  mockEmitter,
		}

		result, err := n.checkNICs(nics)
		assert.NoError(t, err)
		assert.Len(t, result.HealthyNICs, 1)
		assert.Len(t, result.UnhealthyNICs, 1)
	})

	t.Run("Health check error", func(t *testing.T) {
		t.Parallel()
		mockChecker := new(MockNICHealthChecker)
		mockChecker.On("CheckHealth", mock.Anything).Return(false, errors.New("mock error"))

		checkers := map[string]checker.NICHealthChecker{"mockChecker": mockChecker}
		nics := []machine.InterfaceInfo{{Iface: "eth0"}}
		n := &nicManagerImpl{
			checkers: checkers,
			emitter:  mockEmitter,
		}

		result, err := n.checkNICs(nics)
		assert.Error(t, err)
		assert.Nil(t, result)
	})
}
