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

import "time"

type NPDConfig struct {
	NPDIndicatorPlugins []string

	EnableScopeDuplicated bool

	SyncWorkers int

	*LoadAwarePluginConfig
}

func NewNPDConfig() *NPDConfig {
	return &NPDConfig{
		NPDIndicatorPlugins:   []string{},
		EnableScopeDuplicated: false,
		SyncWorkers:           1,
		LoadAwarePluginConfig: &LoadAwarePluginConfig{},
	}
}

type LoadAwarePluginConfig struct {
	// number of workers to sync node metrics
	Workers int
	// time interval of sync node metrics
	SyncMetricInterval time.Duration
	// timeout of list metrics from apiserver
	ListMetricTimeout time.Duration

	// pod selector for checking if pod usage is required
	PodUsageSelectorNamespace string
	PodUsageSelectorKey       string
	PodUsageSelectorVal       string

	MaxPodUsageCount int
}
