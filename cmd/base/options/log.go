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

package options

import (
	"github.com/spf13/pflag"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type LogsOptions struct {
	LogPackageLevel     general.LoggingPKG
	LogFileMaxSizeInMB  uint64
	SupportAsyncLogging bool
	LogDir              string
	LogBufferSizeMB     int
}

func NewLogsOptions() *LogsOptions {
	return &LogsOptions{
		LogPackageLevel:    general.LoggingPKGFull,
		LogFileMaxSizeInMB: 1800,
		LogDir:             "/opt/tiger/toutiao/log/app",
		LogBufferSizeMB:    10000,
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *LogsOptions) AddFlags(fs *pflag.FlagSet) {
	fs.Var(&o.LogPackageLevel, "logs-package-level", "the default package level for logging")
	fs.Uint64Var(&o.LogFileMaxSizeInMB, "log-file-max-size", o.LogFileMaxSizeInMB, "Max size of klog file in MB.")
	fs.BoolVar(&o.SupportAsyncLogging, "support-async-logging", o.SupportAsyncLogging, "whether to support async logging")
	fs.StringVar(&o.LogDir, "log-dir", o.LogDir, "directory of log file")
	fs.IntVar(&o.LogBufferSizeMB, "log-buffer-size", o.LogBufferSizeMB, "size of the ring buffer to store async logs")
}

func (o *LogsOptions) ApplyTo(c *generic.LogConfiguration) error {
	general.SetDefaultLoggingPackage(o.LogPackageLevel)
	general.SetLogFileMaxSize(o.LogFileMaxSizeInMB)
	c.SupportAsyncLogging = o.SupportAsyncLogging
	c.LogDir = o.LogDir
	c.LogFileMaxSize = int(o.LogFileMaxSizeInMB)
	c.LogBufferSizeMB = o.LogBufferSizeMB
	return nil
}
