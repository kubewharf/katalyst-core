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

	"github.com/spf13/pflag"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/sysadvisor/qosaware/reporter"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// HeadroomReporterOptions holds the configurations for headroom reporter in qos aware plugin
type HeadroomReporterOptions struct {
	HeadroomReporterSyncPeriod           time.Duration
	HeadroomReporterSlidingWindowTime    time.Duration
	HeadroomReporterSlidingWindowMinStep general.ResourceList
	HeadroomReporterSlidingWindowMaxStep general.ResourceList

	*CPUHeadroomManagerOptions
	*MemoryHeadroomManagerOptions
}

// NewHeadroomReporterOptions creates new Options with default config
func NewHeadroomReporterOptions() *HeadroomReporterOptions {
	return &HeadroomReporterOptions{
		HeadroomReporterSyncPeriod:        30 * time.Second,
		HeadroomReporterSlidingWindowTime: 2 * time.Minute,
		HeadroomReporterSlidingWindowMinStep: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:    resource.MustParse("0.3"),
			v1.ResourceMemory: resource.MustParse("300Mi"),
		},
		HeadroomReporterSlidingWindowMaxStep: map[v1.ResourceName]resource.Quantity{
			v1.ResourceCPU:    resource.MustParse("4"),
			v1.ResourceMemory: resource.MustParse("5Gi"),
		},
		CPUHeadroomManagerOptions:    NewCPUHeadroomManagerOptions(),
		MemoryHeadroomManagerOptions: NewMemoryHeadroomManagerOptions(),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *HeadroomReporterOptions) AddFlags(fs *pflag.FlagSet) {
	fs.DurationVar(&o.HeadroomReporterSyncPeriod, "headroom-reporter-sync-period", o.HeadroomReporterSyncPeriod,
		"period for headroom managers to sync headroom resource")
	fs.DurationVar(&o.HeadroomReporterSlidingWindowTime, "headroom-reporter-sliding-window-time", o.HeadroomReporterSlidingWindowTime,
		"the window duration for smoothing headroom resource sample")
	fs.Var(&o.HeadroomReporterSlidingWindowMinStep, "headroom-reporter-sliding-window-min-step",
		"the min step headroom resource need to change")
	fs.Var(&o.HeadroomReporterSlidingWindowMaxStep, "headroom-reporter-sliding-window-max-step",
		"the max step headroom resource can change")

	o.CPUHeadroomManagerOptions.AddFlags(fs)
	o.MemoryHeadroomManagerOptions.AddFlags(fs)
}

// ApplyTo fills up config with options
func (o *HeadroomReporterOptions) ApplyTo(c *reporter.HeadroomReporterConfiguration) error {
	c.HeadroomReporterSyncPeriod = o.HeadroomReporterSyncPeriod
	c.HeadroomReporterSlidingWindowTime = o.HeadroomReporterSlidingWindowTime
	c.HeadroomReporterSlidingWindowMinStep = v1.ResourceList(o.HeadroomReporterSlidingWindowMinStep)
	c.HeadroomReporterSlidingWindowMaxStep = v1.ResourceList(o.HeadroomReporterSlidingWindowMaxStep)

	var errList []error
	errList = append(errList, o.CPUHeadroomManagerOptions.ApplyTo(c.CPUHeadroomManagerConfiguration))
	errList = append(errList, o.MemoryHeadroomManagerOptions.ApplyTo(c.MemoryHeadroomManagerConfiguration))

	return errors.NewAggregate(errList)
}

type CPUHeadroomManagerOptions struct{}

func NewCPUHeadroomManagerOptions() *CPUHeadroomManagerOptions {
	return &CPUHeadroomManagerOptions{}
}

// AddFlags adds flags to the specified FlagSet.
func (o *CPUHeadroomManagerOptions) AddFlags(*pflag.FlagSet) {
}

// ApplyTo fills up config with options
func (o *CPUHeadroomManagerOptions) ApplyTo(*reporter.CPUHeadroomManagerConfiguration) error {
	return nil
}

type MemoryHeadroomManagerOptions struct{}

func NewMemoryHeadroomManagerOptions() *MemoryHeadroomManagerOptions {
	return &MemoryHeadroomManagerOptions{}
}

// AddFlags adds flags to the specified FlagSet.
func (o *MemoryHeadroomManagerOptions) AddFlags(*pflag.FlagSet) {}

// ApplyTo fills up config with options
func (o *MemoryHeadroomManagerOptions) ApplyTo(*reporter.MemoryHeadroomManagerConfiguration) error {
	return nil
}
