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

type SeverityName string

const (
	InfoSeverity    SeverityName = "INFO"
	WarningSeverity SeverityName = "WARNING"
	ErrorSeverity   SeverityName = "ERROR"
	FatalSeverity   SeverityName = "FATAL"
)

const (
	metricsNameNumDroppedInfoLogs    = "number_of_dropped_info_logs"
	metricsNameNumDroppedWarningLogs = "number_of_dropped_warning_logs"
	metricsNameNumDroppedErrorLogs   = "number_of_dropped_error_logs"
	metricsNameNumDroppedFatalLogs   = "number_of_dropped_fatal_logs"
)

const (
	defaultInfoLogFileName    = "/opt/tiger/toutiao/log/app/agent.info.log"
	defaultWarningLogFileName = "/opt/tiger/toutiao/log/app/agent.warning.log"
	defaultErrorLogFileName   = "/opt/tiger/toutiao/log/app/agent.error.log"
	defaultFatalLogFileName   = "/opt/tiger/toutiao/log/app/agent.fatal.log"
)

type logInfo struct {
	fileName    string
	metricsName string
}

var logInfoMap = map[SeverityName]*logInfo{
	InfoSeverity:    {fileName: defaultInfoLogFileName, metricsName: metricsNameNumDroppedInfoLogs},
	WarningSeverity: {fileName: defaultWarningLogFileName, metricsName: metricsNameNumDroppedWarningLogs},
	ErrorSeverity:   {fileName: defaultErrorLogFileName, metricsName: metricsNameNumDroppedErrorLogs},
	FatalSeverity:   {fileName: defaultFatalLogFileName, metricsName: metricsNameNumDroppedFatalLogs},
}

type AsyncLogger struct {
	diodeWriters []diode.Writer
	logFile      string
}

// NewAsyncLogger creates an async logger that produces an async writer for each of the severity levels.
// The async writer spins up a goroutine that periodically flushes the buffered logs to disk.
func NewAsyncLogger(agentCtx *agent.GenericContext, maxSizeMB int, bufferSizeMB int) *AsyncLogger {
	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter()

	asyncLogger := &AsyncLogger{}
	for severity, logInfo := range logInfoMap {
		// lumberjackLogger is a logger that rotates log files
		lumberjackLogger := &lumberjack.Logger{
			Filename: logInfo.fileName,
			MaxSize:  maxSizeMB,
		}

		// diodeWriter is a writer that stores logs in a ring buffer and asynchronously flushes them
		diodeWriter := diode.NewWriter(lumberjackLogger, bufferSizeMB, 10*time.Millisecond, func(missed int) {
			_ = wrappedEmitter.StoreInt64(logInfo.metricsName, int64(missed), metrics.MetricTypeNameRaw)
		})
		// Overrides the default synchronous writer with the diode writer
		klog.SetOutputBySeverity(string(severity), diodeWriter)
		asyncLogger.diodeWriters = append(asyncLogger.diodeWriters, diodeWriter)
	}

	return asyncLogger
}

func (a *AsyncLogger) Shutdown() {
	klog.Info("[Shutdown] async writer is shutting down...")
	klog.Flush()
	for _, writer := range a.diodeWriters {
		writer.Close()
	}
}
