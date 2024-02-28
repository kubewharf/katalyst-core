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

package metaserver

import (
	"time"

	"golang.org/x/time/rate"
)

type MetricConfiguration struct {
	MetricInsurancePeriod time.Duration
}

type PodConfiguration struct {
	KubeletPodCacheSyncPeriod    time.Duration
	KubeletPodCacheSyncMaxRate   rate.Limit
	KubeletPodCacheSyncBurstBulk int

	RuntimePodCacheSyncPeriod time.Duration
}

type NodeConfiguration struct{}

type CNRConfiguration struct {
	CNRCacheTTL time.Duration
}

type CNCConfiguration struct {
	CustomNodeConfigCacheTTL time.Duration
}

type AgentConfiguration struct {
	*MetricConfiguration
	*PodConfiguration
	*NodeConfiguration
	*CNRConfiguration
	*CNCConfiguration

	EnableMetricsFetcher bool
	EnableCNCFetcher     bool
}

func NewAgentConfiguration() *AgentConfiguration {
	return &AgentConfiguration{
		MetricConfiguration: &MetricConfiguration{},
		PodConfiguration:    &PodConfiguration{},
		NodeConfiguration:   &NodeConfiguration{},
		CNRConfiguration:    &CNRConfiguration{},
		CNCConfiguration:    &CNCConfiguration{},
	}
}
