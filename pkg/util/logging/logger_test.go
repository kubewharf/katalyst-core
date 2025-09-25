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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"

	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	metrics_pool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
)

func TestNewAsyncLogger(t *testing.T) {
	t.Parallel()
	agentCtx := &agent.GenericContext{
		GenericContext: &katalyst_base.GenericContext{
			EmitterPool: metrics_pool.DummyMetricsEmitterPool{},
		},
	}
	tempDir := t.TempDir()
	logFilePath := filepath.Join(tempDir, "test.log")
	asyncLogger := NewAsyncLogger(agentCtx, logFilePath, 100, 100)
	assert.NotNil(t, asyncLogger)
}

func TestAsyncLogger_WriteAndShutdown(t *testing.T) {
	t.Parallel()
	agentCtx := &agent.GenericContext{
		GenericContext: &katalyst_base.GenericContext{
			EmitterPool: metrics_pool.DummyMetricsEmitterPool{},
		},
	}
	tempDir := t.TempDir()
	logFilePath := filepath.Join(tempDir, "test.log")
	asyncLogger := NewAsyncLogger(agentCtx, logFilePath, 100, 100)
	assert.NotNil(t, asyncLogger)

	_, err := asyncLogger.Write([]byte("test"))
	assert.NoError(t, err, "write log should not fail")

	asyncLogger.Shutdown()

	_, err = os.Stat(logFilePath)
	assert.NoError(t, err, "log file should be created")
}
