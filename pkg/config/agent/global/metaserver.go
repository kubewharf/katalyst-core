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

package global

import (
	"time"

	"golang.org/x/time/rate"
)

type MetaServerConfiguration struct {
	CNRCacheTTL                    time.Duration
	CustomNodeConfigCacheTTL       time.Duration
	ServiceProfileCacheTTL         time.Duration
	ConfigCacheTTL                 time.Duration
	ConfigSkipFailedInitialization bool
	ConfigDisableDynamic           bool
	ConfigCheckpointGraceTime      time.Duration

	KubeletReadOnlyPort          int
	KubeletSecurePort            int
	EnableKubeletSecurePort      bool
	KubeletPodCacheSyncPeriod    time.Duration
	KubeletPodCacheSyncMaxRate   rate.Limit
	KubeletPodCacheSyncBurstBulk int
	KubeletConfigEndpoint        string
	KubeletPodsEndpoint          string
	KubeletSummaryEndpoint       string
	APIAuthTokenFile             string

	RemoteRuntimeEndpoint     string
	RuntimePodCacheSyncPeriod time.Duration

	CheckpointManagerDir string

	EnableMetricsFetcher bool
	EnableCNCFetcher     bool

	MetricInsurancePeriod time.Duration
}

func NewMetaServerConfiguration() *MetaServerConfiguration {
	return &MetaServerConfiguration{}
}
