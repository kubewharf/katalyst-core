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
	"time"

	"github.com/rs/zerolog/diode"
	"gopkg.in/natefinch/lumberjack.v2"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	metricsNameNumDroppedLogs = "number_of_dropped_logs"
)

type AsyncLogger struct {
	diodeWriter diode.Writer
	logFile     string
}

func NewAsyncLogger(agentCtx *agent.GenericContext, logFile string, maxSizeMB int, bufferSizeMB int) *AsyncLogger {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter()
	// lumberjackLogger is a logger that rotates log files
	lumberjackLogger := &lumberjack.Logger{
		Filename: logFile,
		MaxSize:  maxSizeMB,
	}
	asyncWriter := &AsyncLogger{
		logFile: logFile,
	}

	// diodeWriter is a writer that stores logs in a ring buffer and asynchronously flushes them
	diodeWriter := diode.NewWriter(lumberjackLogger, bufferSizeMB, 10*time.Millisecond, func(missed int) {
		_ = wrappedEmitter.StoreInt64(metricsNameNumDroppedLogs, int64(missed), metrics.MetricTypeNameRaw)
	})

	asyncWriter.diodeWriter = diodeWriter
	// Overrides the default synchronous writer with the diode writer
	klog.SetOutput(diodeWriter)
	return asyncWriter
}

func (a *AsyncLogger) Write(p []byte) (n int, err error) {
	return a.diodeWriter.Write(p)
}

func (a *AsyncLogger) Shutdown() {
	klog.Info("[Shutdown] async writer is shutting down...")
	klog.Flush()
	a.diodeWriter.Close()
}
