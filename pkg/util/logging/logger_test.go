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

package logging

import (
	"testing"

	"github.com/stretchr/testify/assert"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	metrics_pool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func TestNewCustomLogger(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name                      string
		logDir                    string
		maxSizeMB                 int
		maxAge                    int
		maxBackups                int
		bufferSize                int
		expectedDiodeWritersCount int
	}{
		{
			name:                      "Disabled logger (logDir is empty), logger is nil",
			logDir:                    "",
			maxSizeMB:                 10,
			maxAge:                    0,
			maxBackups:                10,
			bufferSize:                1024,
			expectedDiodeWritersCount: 0,
		},
		{
			name:                      "Synchronous logger (bufferSize=0)",
			logDir:                    t.TempDir(),
			maxSizeMB:                 1,
			maxAge:                    1,
			maxBackups:                1,
			bufferSize:                0,
			expectedDiodeWritersCount: 0,
		},
		{
			name:                      "Asynchronous logger",
			logDir:                    t.TempDir(),
			maxSizeMB:                 1,
			maxAge:                    1,
			maxBackups:                1,
			bufferSize:                1024,
			expectedDiodeWritersCount: 4,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			agentCtx := &agent.GenericContext{
				GenericContext: &katalyst_base.GenericContext{
					EmitterPool: metrics_pool.DummyMetricsEmitterPool{},
				},
			}

			logger := NewCustomLogger(agentCtx, tc.logDir, tc.maxSizeMB, tc.maxAge, tc.maxBackups, tc.bufferSize)

			assert.NotNil(t, logger)
			assert.Len(t, logger.diodeWriters, tc.expectedDiodeWritersCount)
			assert.NotPanics(t, func() {
				logger.Shutdown()
			})
		})
	}
}
