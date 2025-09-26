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

const (
	MetricProvisionerMalachite = "malachite"

	// MetricProvisionerMalachiteRealtime is for power metric due to historical reason
	// todo: consider rename it to malachite_realtime_power
	MetricProvisionerMalachiteRealtime     = "malachite_realtime"
	MetricProvisionerMalachiteRealtimeFreq = "malachite_realtime_freq"

	MetricProvisionerCgroup  = "cgroup"
	MetricProvisionerKubelet = "kubelet"
	MetricProvisionerRodan   = "rodan"
)

type MetricConfiguration struct {
	MetricInsurancePeriod time.Duration
	MetricProvisions      []string

	DefaultInterval      time.Duration
	ProvisionerIntervals map[string]time.Duration

	*MalachiteMetricConfiguration
	*CgroupMetricConfiguration
	*KubeletMetricConfiguration
	*RodanMetricConfiguration
}

type MalachiteMetricConfiguration struct{}

type CgroupMetricConfiguration struct{}

type KubeletMetricConfiguration struct{}

type RodanMetricConfiguration struct {
	RodanServerPort int
}

type PodConfiguration struct {
	KubeletPodCacheSyncPeriod         time.Duration
	KubeletPodCacheSyncMaxRate        rate.Limit
	KubeletPodCacheSyncBurstBulk      int
	KubeletPodCacheSyncEmptyThreshold int

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
	EnableNPDFetcher     bool
}

func NewAgentConfiguration() *AgentConfiguration {
	return &AgentConfiguration{
		MetricConfiguration: &MetricConfiguration{
			ProvisionerIntervals:         make(map[string]time.Duration),
			MalachiteMetricConfiguration: &MalachiteMetricConfiguration{},
			CgroupMetricConfiguration:    &CgroupMetricConfiguration{},
			KubeletMetricConfiguration:   &KubeletMetricConfiguration{},
			RodanMetricConfiguration:     &RodanMetricConfiguration{},
		},
		PodConfiguration:  &PodConfiguration{},
		NodeConfiguration: &NodeConfiguration{},
		CNRConfiguration:  &CNRConfiguration{},
		CNCConfiguration:  &CNCConfiguration{},
	}
}
