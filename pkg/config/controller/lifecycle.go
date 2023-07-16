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

	"k8s.io/apimachinery/pkg/labels"
)

type CNRLifecycleConfig struct{}

type CNCLifecycleConfig struct{}

type HealthzConfig struct {
	DryRun        bool
	NodeSelector  labels.Selector
	AgentSelector map[string]labels.Selector

	// config for checking logic
	CheckWindow           time.Duration
	UnhealthyPeriods      time.Duration
	AgentUnhealthyPeriods map[string]time.Duration

	// config for handling logic
	HandlePeriod  time.Duration
	AgentHandlers map[string]string

	// config for disrupting logic
	TaintQPS                 float32
	EvictQPS                 float32
	DisruptionTaintThreshold float32
	DisruptionEvictThreshold float32
}

type LifeCycleConfig struct {
	EnableHealthz      bool
	EnableCNCLifecycle bool

	*CNRLifecycleConfig
	*CNCLifecycleConfig
	*HealthzConfig
}

func NewLifeCycleConfig() *LifeCycleConfig {
	return &LifeCycleConfig{
		EnableHealthz:      false,
		EnableCNCLifecycle: true,
		CNRLifecycleConfig: &CNRLifecycleConfig{},
		CNCLifecycleConfig: &CNCLifecycleConfig{},
		HealthzConfig:      &HealthzConfig{},
	}
}
