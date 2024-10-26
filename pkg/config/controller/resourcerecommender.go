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

package controller

import (
	"time"

	"github.com/kubewharf/katalyst-core/pkg/util/datasource/prometheus"
)

type ResourceRecommenderConfig struct {
	OOMRecordMaxNumber int

	HealthProbeBindPort string
	MetricsBindPort     string

	// available datasource: prom
	DataSource []string
	// DataSourcePromConfig is the prometheus datasource config
	DataSourcePromConfig prometheus.PromConfig

	// LogVerbosityLevel to specify log verbosity level. (The default level is 4)
	// Set it to something larger than 4 if more detailed logs are needed.
	LogVerbosityLevel string

	// number of workers to sync
	RecSyncWorkers int
	RecSyncPeriod  time.Duration
}

func NewResourceRecommenderConfig() *ResourceRecommenderConfig {
	return &ResourceRecommenderConfig{}
}
