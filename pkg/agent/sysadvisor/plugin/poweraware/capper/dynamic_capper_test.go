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

package capper

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

type mockCapper struct {
	mu         sync.Mutex
	resetCalls int
	capCalls   int
}

func (m *mockCapper) Init() error  { return nil }
func (m *mockCapper) Start() error { return nil }
func (m *mockCapper) Stop() error  { return nil }

func (m *mockCapper) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.resetCalls++
}

func (m *mockCapper) Cap(_ context.Context, _, _ int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.capCalls++
}

func (m *mockCapper) snapshot() (reset, cap int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resetCalls, m.capCalls
}

func newDynamicConf(disablePowerCapping bool) *dynamic.DynamicAgentConfiguration {
	c := dynamic.NewDynamicAgentConfiguration()
	conf := c.GetDynamicConfiguration()
	conf.DisablePowerCapping = disablePowerCapping
	c.SetDynamicConfiguration(conf)
	return c
}

func TestDynamicPowerCapper_Cap(t *testing.T) {
	t.Parallel()

	type step struct {
		targetWatts int
		currWatt    int
	}
	type expected struct {
		resetCalls int
		capCalls   int
	}
	tests := []struct {
		name         string
		initDisabled bool
		steps        []step
		want         []expected // expected after each step
	}{
		{
			name:         "enabled delegates all caps",
			initDisabled: false,
			steps: []step{
				{targetWatts: 100, currWatt: 120},
				{targetWatts: 90, currWatt: 110},
			},
			want: []expected{
				{resetCalls: 0, capCalls: 1},
				{resetCalls: 0, capCalls: 2},
			},
		},
		{
			name:         "disabled resets once then skips",
			initDisabled: true,
			steps: []step{
				{targetWatts: 100, currWatt: 120},
				{targetWatts: 90, currWatt: 110},
				{targetWatts: 80, currWatt: 100},
			},
			want: []expected{
				{resetCalls: 1, capCalls: 0},
				{resetCalls: 1, capCalls: 0},
				{resetCalls: 1, capCalls: 0},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock := &mockCapper{}
			conf := newDynamicConf(tt.initDisabled)
			d := NewDynamicPowerCapper(conf, mock)

			for i, s := range tt.steps {
				d.Cap(context.Background(), s.targetWatts, s.currWatt)

				r, c := mock.snapshot()
				assert.Equal(t, tt.want[i].resetCalls, r, "step %d: resetCalls", i)
				assert.Equal(t, tt.want[i].capCalls, c, "step %d: capCalls", i)
			}
		})
	}
}
