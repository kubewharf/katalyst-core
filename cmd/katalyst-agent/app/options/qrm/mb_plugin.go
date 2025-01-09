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

package qrm

import (
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy/ccdtarget"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/policy/strategy/domaintarget"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

const defaultIncubationInterval = time.Second * 30

type MBOptions struct {
	// shared meta group related (it could have several subgroups like shared-50, shared-30)
	CPUSetPoolToSharedSubgroup map[string]int

	// mb resource allocation and policy related
	MinMBPerCCD      int
	DomainMBCapacity int
	MBRemoteLimit    int

	// socket (top qos) mb reservation related
	IncubationInterval time.Duration

	// leaf (lowest qos) mb planner related
	LeafThrottleType    string
	LeafEaseType        string
	MBPressureThreshold int
	MBEaseThreshold     int

	CCDMBDistributorType string

	// incoming (recipient view) to outgoing (sender view) mapping related
	SourcerType string
}

func NewMBOptions() *MBOptions {
	return &MBOptions{
		IncubationInterval: defaultIncubationInterval,
		CPUSetPoolToSharedSubgroup: map[string]int{
			"batch": 30,
			"flink": 30,
			"share": 50,
		},
		MinMBPerCCD:      4_000,   // 4 GB
		DomainMBCapacity: 122_000, // 122_000 MBps = 122 GBps
		MBRemoteLimit:    20_000,  // 20 GB

		LeafThrottleType: string(domaintarget.ExtremeThrottle),
		LeafEaseType:     string(domaintarget.HalfEase),

		MBPressureThreshold: 6_000,
		MBEaseThreshold:     9_000,

		CCDMBDistributorType: string(ccdtarget.LogarithmicScaleDistributor),

		SourcerType: "adaptive-grbs",
	}
}

func (m *MBOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("mb_resource_plugin")
	fs.DurationVar(&m.IncubationInterval, "mb-incubation-interval", m.IncubationInterval,
		"time to protect socket pod before it is fully exercise memory bandwidth")
	fs.StringToIntVar(&m.CPUSetPoolToSharedSubgroup, "cpuset-pool-to-shared-subgroup", m.CPUSetPoolToSharedSubgroup,
		"mapping from cpuset pool name to shared_xx")
	fs.IntVar(&m.MinMBPerCCD, "min-mb-per-ccd", m.MinMBPerCCD, "lower bound of MB per ccd in MBps")
	fs.IntVar(&m.DomainMBCapacity, "domain-mb-capacity", m.DomainMBCapacity, "MB capacity per domain(socket) in MBps")
	fs.IntVar(&m.MBRemoteLimit, "mb-remote-limit", m.MBRemoteLimit, "upper bound limit from remote if high QoS workload is running")
	fs.StringVar(&m.LeafThrottleType, "mb-leaf-throttle-type", m.LeafThrottleType, "type of shared-30 throttle planner")
	fs.StringVar(&m.LeafEaseType, "mb-leaf-ease-type", m.LeafEaseType, "type of shared-30 ease planner")
	fs.IntVar(&m.MBPressureThreshold, "mb-pressure-threshold", m.MBPressureThreshold, "the threshold below which a domain available mb is would try to throttle leaf qos workloads")
	fs.IntVar(&m.MBEaseThreshold, "mb-ease-threshold", m.MBEaseThreshold, "the threshold above which a domain available mb is would try to ease leaf qos workloads")
	fs.StringVar(&m.CCDMBDistributorType, "mb-ccd-distributor", m.CCDMBDistributorType, "type of ccd mb planner")
	fs.StringVar(&m.SourcerType, "mb-sourcer-type", m.SourcerType, "type of mb target source distributor")
}

func (m *MBOptions) ApplyTo(conf *qrmconfig.MBQRMPluginConfig) error {
	conf.IncubationInterval = m.IncubationInterval
	conf.CPUSetPoolToSharedSubgroup = m.CPUSetPoolToSharedSubgroup
	conf.MinMBPerCCD = m.MinMBPerCCD
	conf.DomainMBCapacity = m.DomainMBCapacity
	conf.MBRemoteLimit = m.MBRemoteLimit

	// todo: to validate assignments
	conf.LeafThrottleType = m.LeafThrottleType
	conf.LeafEaseType = m.LeafEaseType

	conf.MBPressureThreshold = m.MBPressureThreshold
	conf.MBEaseThreshold = m.MBEaseThreshold

	conf.CCDMBDistributorType = m.CCDMBDistributorType

	conf.SourcerType = m.SourcerType
	return nil
}
