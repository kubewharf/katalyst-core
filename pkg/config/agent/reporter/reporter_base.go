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

package reporter

import (
	"time"
)

type GenericReporterConfiguration struct {
	CollectInterval time.Duration

	// InnerPlugins is the list of plugins implemented in katalyst to enable or disable
	// '*' means "all enabled by default"
	// 'foo' means "enable 'foo'"
	// '-foo' means "disable 'foo'"
	// first item for a particular name wins
	InnerPlugins []string

	RefreshLatestCNRPeriod time.Duration

	// DefaultCNRLabels is the labels for CNR created by reporter
	DefaultCNRLabels map[string]string
}

type ReporterPluginsConfiguration struct {
	*KubeletPluginConfiguration
}

func NewGenericReporterConfiguration() *GenericReporterConfiguration {
	return &GenericReporterConfiguration{}
}

func NewReporterPluginsConfiguration() *ReporterPluginsConfiguration {
	return &ReporterPluginsConfiguration{
		KubeletPluginConfiguration: NewKubeletPluginConfiguration(),
	}
}
