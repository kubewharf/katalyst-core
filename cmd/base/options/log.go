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

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type LogsOptions struct {
	LogPackageLevel general.LoggingPKG
}

func NewLogsOptions() *LogsOptions {
	return &LogsOptions{
		LogPackageLevel: general.LoggingPKGFull,
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *LogsOptions) AddFlags(fs *pflag.FlagSet) {
	fs.Var(&o.LogPackageLevel, "logs-package-level", "the default package level for logging")
}

func (o *LogsOptions) ApplyTo() error {
	general.SetDefaultLoggingPackage(o.LogPackageLevel)
	return nil
}
