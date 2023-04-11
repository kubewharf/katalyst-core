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

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

// HeadroomReporterConfiguration stores configurations of headroom reporters in qos aware plugin
type HeadroomReporterConfiguration struct {
	HeadroomReporterSyncPeriod           time.Duration
	HeadroomReporterSlidingWindowTime    time.Duration
	HeadroomReporterSlidingWindowMinStep v1.ResourceList
	HeadroomReporterSlidingWindowMaxStep v1.ResourceList

	*CPUHeadroomManagerConfiguration
	*MemoryHeadroomManagerConfiguration
}

// NewHeadroomReporterConfiguration creates new headroom reporter configurations
func NewHeadroomReporterConfiguration() *HeadroomReporterConfiguration {
	return &HeadroomReporterConfiguration{
		HeadroomReporterSlidingWindowMinStep: v1.ResourceList{},
		HeadroomReporterSlidingWindowMaxStep: v1.ResourceList{},
		CPUHeadroomManagerConfiguration:      NewCPUHeadroomManagerConfiguration(),
		MemoryHeadroomManagerConfiguration:   NewMemoryHeadroomManagerConfiguration(),
	}
}

// ApplyConfiguration is used to set configuration based on conf.
func (c *HeadroomReporterConfiguration) ApplyConfiguration(*HeadroomReporterConfiguration, *dynamic.DynamicConfigCRD) {
}

type CPUHeadroomManagerConfiguration struct {
	CPUBroker string
}

func NewCPUHeadroomManagerConfiguration() *CPUHeadroomManagerConfiguration {
	return &CPUHeadroomManagerConfiguration{}
}

type MemoryHeadroomManagerConfiguration struct {
	MemoryBroker string
}

func NewMemoryHeadroomManagerConfiguration() *MemoryHeadroomManagerConfiguration {
	return &MemoryHeadroomManagerConfiguration{}
}
