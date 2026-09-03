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
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/nic/checker"
	nicfilter "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/nic/filter"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type MockNICHealthChecker struct {
	mock.Mock
}

type mockNICFilter struct{}

var defaultRegistryMu sync.Mutex

func (m *mockNICFilter) Filter(nics []machine.InterfaceInfo) ([]machine.InterfaceInfo, error) {
	filtered := make([]machine.InterfaceInfo, 0, len(nics))
	for _, nic := range nics {
		if nic.Name == "eth1" {
			filtered = append(filtered, nic)
		}
	}
	return filtered, nil
}

type filterFunc func(nics []machine.InterfaceInfo) ([]machine.InterfaceInfo, error)

func (f filterFunc) Filter(nics []machine.InterfaceInfo) ([]machine.InterfaceInfo, error) {
	return f(nics)
}

type metricSample struct {
	key  string
	val  int64
	tags map[string]string
}

type recordMetricEmitter struct {
	samples []metricSample
}

func (r *recordMetricEmitter) StoreInt64(key string, val int64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	sample := metricSample{
		key:  key,
		val:  val,
		tags: make(map[string]string, len(tags)),
	}
	for _, tag := range tags {
		sample.tags[tag.Key] = tag.Val
	}
	r.samples = append(r.samples, sample)
	return nil
}

func (r *recordMetricEmitter) StoreFloat64(_ string, _ float64, _ metrics.MetricTypeName, _ ...metrics.MetricTag) error {
	return nil
}

func (r *recordMetricEmitter) WithTags(_ string, _ ...metrics.MetricTag) metrics.MetricEmitter {
	return r
}

func (r *recordMetricEmitter) Run(_ context.Context) {}

func (m *MockNICHealthChecker) CheckHealth(nic machine.InterfaceInfo) (bool, error) {
	args := m.Called(nic)
	return args.Bool(0), args.Error(1)
}

func TestNICKey(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		nic  machine.InterfaceInfo
		want string
	}{
		{
			name: "empty namespace",
			nic:  machine.InterfaceInfo{Name: "eth0"},
			want: "eth0",
		},
		{
			name: "non-empty namespace",
			nic:  machine.InterfaceInfo{Name: "eth0", NetNSInfo: machine.NetNSInfo{NSName: "ns1"}},
			want: "ns1/eth0",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, nicKey(tc.nic))
		})
	}
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

func TestNewNICManagerWithAllocatableNICFilter(t *testing.T) {
	t.Parallel()

	const filterName = "keep-eth1"
	defaultRegistryMu.Lock()
	err := nicfilter.DefaultRegistry.Register(filterName, func() (nicfilter.AllocatableNICFilter, error) {
		return &mockNICFilter{}, nil
	})
	assert.NoError(t, err)
	defer func() {
		delete(nicfilter.DefaultRegistry, filterName)
		defaultRegistryMu.Unlock()
	}()

	ipv4 := net.ParseIP("192.168.0.2")
	mockMetaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				ExtraNetworkInfo: &machine.ExtraNetworkInfo{
					Interface: []machine.InterfaceInfo{
						{Name: "eth0", Enable: true, Addr: &machine.IfaceAddr{IPV4: []*net.IP{&ipv4}}},
						{Name: "eth1", Enable: true, Addr: &machine.IfaceAddr{IPV4: []*net.IP{&ipv4}}},
					},
				},
			},
		},
	}
	mockEmitter := &recordMetricEmitter{}
	mockConf, err := options.NewOptions().Config()
	assert.NoError(t, err)
	mockConf.NICFilters = []string{filterName}

	manager, err := NewNICManager(mockMetaServer, mockEmitter, mockConf)
	assert.NoError(t, err)

	nics := manager.GetNICs()
	assert.Len(t, nics.HealthyNICs, 1)
	assert.Equal(t, "eth1", nics.HealthyNICs[0].Name)
	assert.True(t, hasMetricSample(mockEmitter.samples, "network_plugin_nic_filter", 1, map[string]string{"filter": filterName, "nic": "eth0", "result": "filtered"}))
	assert.True(t, hasMetricSample(mockEmitter.samples, "network_plugin_nic_filter", 1, map[string]string{"filter": filterName, "nic": "eth1", "result": "kept"}))
}

func TestNewNICManagerWithAllocatableNICFilterError(t *testing.T) {
	t.Parallel()

	newMetaServer := func() *metaserver.MetaServer {
		return &metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				KatalystMachineInfo: &machine.KatalystMachineInfo{
					ExtraNetworkInfo: &machine.ExtraNetworkInfo{
						Interface: []machine.InterfaceInfo{
							{Name: "eth0", Enable: true},
						},
					},
				},
			},
		}
	}

	t.Run("factory error", func(t *testing.T) {
		t.Parallel()

		const filterName = "factory-error"
		expectedErr := errors.New("factory error")
		defaultRegistryMu.Lock()
		err := nicfilter.DefaultRegistry.Register(filterName, func() (nicfilter.AllocatableNICFilter, error) {
			return nil, expectedErr
		})
		assert.NoError(t, err)
		defer func() {
			delete(nicfilter.DefaultRegistry, filterName)
			defaultRegistryMu.Unlock()
		}()

		mockConf, err := options.NewOptions().Config()
		assert.NoError(t, err)
		mockConf.NICFilters = []string{filterName}

		manager, err := NewNICManager(newMetaServer(), &metrics.DummyMetrics{}, mockConf)
		assert.ErrorIs(t, err, expectedErr)
		assert.Nil(t, manager)
	})

	t.Run("filter error", func(t *testing.T) {
		t.Parallel()

		const filterName = "filter-error"
		expectedErr := errors.New("filter error")
		defaultRegistryMu.Lock()
		err := nicfilter.DefaultRegistry.Register(filterName, func() (nicfilter.AllocatableNICFilter, error) {
			return filterFunc(func([]machine.InterfaceInfo) ([]machine.InterfaceInfo, error) {
				return nil, expectedErr
			}), nil
		})
		assert.NoError(t, err)
		defer func() {
			delete(nicfilter.DefaultRegistry, filterName)
			defaultRegistryMu.Unlock()
		}()

		mockConf, err := options.NewOptions().Config()
		assert.NoError(t, err)
		mockConf.NICFilters = []string{filterName}

		manager, err := NewNICManager(newMetaServer(), &metrics.DummyMetrics{}, mockConf)
		assert.ErrorIs(t, err, expectedErr)
		assert.Nil(t, manager)
	})
}

func TestFilterAllocatableNICsWithInPlaceFilter(t *testing.T) {
	t.Parallel()

	const filterName = "in-place"
	registry := nicfilter.Registry{
		filterName: func() (nicfilter.AllocatableNICFilter, error) {
			return filterFunc(func(nics []machine.InterfaceInfo) ([]machine.InterfaceInfo, error) {
				nics[0] = nics[1]
				return nics[:1], nil
			}), nil
		},
	}
	emitter := &recordMetricEmitter{}
	filtered, err := filterAllocatableNICs(registry, []machine.InterfaceInfo{
		{Name: "eth0"},
		{Name: "eth1"},
	}, []string{filterName}, emitter)

	assert.NoError(t, err)
	assert.Equal(t, []machine.InterfaceInfo{{Name: "eth1"}}, filtered)
	assert.True(t, hasMetricSample(emitter.samples, "network_plugin_nic_filter", 1, map[string]string{"filter": filterName, "nic": "eth0", "result": "filtered"}))
	assert.True(t, hasMetricSample(emitter.samples, "network_plugin_nic_filter", 1, map[string]string{"filter": filterName, "nic": "eth1", "result": "kept"}))
}

func TestFilterAllocatableNICsPreservesConfiguredOrder(t *testing.T) {
	t.Parallel()

	registry := nicfilter.Registry{
		"first": func() (nicfilter.AllocatableNICFilter, error) {
			return filterFunc(func(nics []machine.InterfaceInfo) ([]machine.InterfaceInfo, error) {
				return nics[1:], nil
			}), nil
		},
		"second": func() (nicfilter.AllocatableNICFilter, error) {
			return filterFunc(func(nics []machine.InterfaceInfo) ([]machine.InterfaceInfo, error) {
				return nics[:1], nil
			}), nil
		},
	}

	filtered, err := filterAllocatableNICs(registry, []machine.InterfaceInfo{
		{Name: "eth0"},
		{Name: "eth1"},
	}, []string{"second", "first"}, nil)

	assert.NoError(t, err)
	assert.Empty(t, filtered)
}

func TestInitAllocatableNICFilters(t *testing.T) {
	t.Parallel()

	newRegistry := func() nicfilter.Registry {
		return nicfilter.Registry{
			"first": func() (nicfilter.AllocatableNICFilter, error) {
				return &mockNICFilter{}, nil
			},
			"second": func() (nicfilter.AllocatableNICFilter, error) {
				return &mockNICFilter{}, nil
			},
			"third": func() (nicfilter.AllocatableNICFilter, error) {
				return &mockNICFilter{}, nil
			},
		}
	}

	t.Run("explicit filters are enabled", func(t *testing.T) {
		t.Parallel()

		filters, err := initAllocatableNICFilters(newRegistry(), []string{"second", "first"})
		assert.NoError(t, err)
		assert.Equal(t, []string{"second", "first"}, filterNames(filters))
	})

	t.Run("wildcard can disable registered filter", func(t *testing.T) {
		t.Parallel()

		filters, err := initAllocatableNICFilters(newRegistry(), []string{"*", "-second"})
		assert.NoError(t, err)
		assert.Equal(t, []string{"first", "third"}, filterNames(filters))
	})

	t.Run("wildcard does not duplicate explicit filter", func(t *testing.T) {
		t.Parallel()

		filters, err := initAllocatableNICFilters(newRegistry(), []string{"*", "second"})
		assert.NoError(t, err)
		assert.Equal(t, []string{"first", "second", "third"}, filterNames(filters))
	})

	t.Run("unknown explicit filter returns error", func(t *testing.T) {
		t.Parallel()

		filters, err := initAllocatableNICFilters(newRegistry(), []string{"missing"})
		assert.Error(t, err)
		assert.Empty(t, filters)
	})
}

func filterNames(filters []namedAllocatableNICFilter) []string {
	names := make([]string, 0, len(filters))
	for _, f := range filters {
		names = append(names, f.Name)
	}
	return names
}

func hasMetricSample(samples []metricSample, key string, val int64, tags map[string]string) bool {
	for _, sample := range samples {
		if sample.key != key || sample.val != val {
			continue
		}

		matched := true
		for k, v := range tags {
			if sample.tags[k] != v {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}

	return false
}

func TestGetNICs(t *testing.T) {
	t.Parallel()

	t.Run("Single NIC", func(t *testing.T) {
		t.Parallel()
		manager := &nicManagerImpl{
			nics: &NICs{
				HealthyNICs: []machine.InterfaceInfo{{Name: "eth0"}},
			},
		}

		nics := manager.GetNICs()
		assert.Len(t, nics.HealthyNICs, 1)
		assert.Equal(t, "eth0", nics.HealthyNICs[0].Name)
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
					{Name: "eth0"},
					{Name: "eth1"},
				},
			},
		}

		nics := manager.GetNICs()
		assert.Len(t, nics.HealthyNICs, 2)
		assert.ElementsMatch(t, []string{"eth0", "eth1"}, []string{nics.HealthyNICs[0].Name, nics.HealthyNICs[1].Name})
	})
}

func TestUpdateNICs(t *testing.T) {
	t.Parallel()

	t.Run("Update with valid NICs", func(t *testing.T) {
		t.Parallel()
		mockChecker := new(MockNICHealthChecker)
		mockChecker.On("CheckHealth", mock.Anything).Return(true, nil)

		conf, err := options.NewOptions().Config()
		assert.NoError(t, err)

		manager := &nicManagerImpl{
			nics: &NICs{},
			defaultAllocatableNICs: []machine.InterfaceInfo{
				{Name: "eth0"},
			},
			checkers: map[string]checker.NICHealthChecker{
				"mockChecker": mockChecker,
			},
			conf: conf,
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		manager.updateNICs(ctx)

		nics := manager.GetNICs()
		assert.Len(t, nics.HealthyNICs, 1)
		assert.Equal(t, "eth0", nics.HealthyNICs[0].Name)
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
				{Name: "eth0"},
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

		conf, err := options.NewOptions().Config()
		assert.NoError(t, err)

		checkers := map[string]checker.NICHealthChecker{"mockChecker": mockChecker}
		nics := []machine.InterfaceInfo{{Name: "eth0"}, {Name: "eth1"}}
		n := &nicManagerImpl{
			checkers:               checkers,
			emitter:                mockEmitter,
			conf:                   conf,
			nicHealthCheckTime:     1,
			nicHealthCheckInterval: 0,
		}

		result, err := n.checkNICs(nics)
		assert.NoError(t, err)
		assert.Len(t, result.HealthyNICs, 2)
		assert.Empty(t, result.UnhealthyNICs)
		assert.Empty(t, result.UnhealthyReasons)
	})

	t.Run("Some NICs unhealthy", func(t *testing.T) {
		t.Parallel()
		mockChecker := new(MockNICHealthChecker)
		mockChecker.On("CheckHealth", mock.Anything).Return(false, nil).Twice()
		mockChecker.On("CheckHealth", mock.Anything).Return(true, nil).Twice()

		conf, err := options.NewOptions().Config()
		assert.NoError(t, err)

		checkers := map[string]checker.NICHealthChecker{"mockChecker": mockChecker}
		nics := []machine.InterfaceInfo{{Name: "eth0"}, {Name: "eth1"}}
		n := &nicManagerImpl{
			checkers:               checkers,
			emitter:                mockEmitter,
			conf:                   conf,
			nicHealthCheckTime:     1,
			nicHealthCheckInterval: 0,
		}

		result, err := n.checkNICs(nics)
		assert.NoError(t, err)
		assert.Len(t, result.HealthyNICs, 1)
		assert.Len(t, result.UnhealthyNICs, 1)
		assert.Len(t, result.UnhealthyReasons, 1)
		assert.Equal(t, result.UnhealthyReasons[0], "health check mockChecker failed for nic eth0")
	})
}
