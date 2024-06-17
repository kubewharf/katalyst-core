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

package monitor

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type MBMonitor struct {
	*machine.KatalystMachineInfo
	Controller  MBController
	Interval    uint64
	Started     bool
	MonitorOnly bool // skip the control process if set to true
	mctx        context.Context
	done        context.CancelFunc
}

type MBController struct {
	MBThresholdPerNUMA  uint64             // MB throttling starts if a physical node's MB bythonds a threshold
	IncreaseStep        uint64             // how much to boost each time when releasing the MB throttling
	DecreaseStep        uint64             // how much to reduce each time when throttling the MB
	SweetPoint          float64            // the point where we start releasing memory bandwidth throttling
	PainPoint           float64            // the point where we start throttling the memory bandwidth
	UnthrottlePoint     float64            // the point where we unthrottle all numas on a package
	CCDCosMap           map[int][]CosEntry // mapping from CCD to its Cos and usage
	RMIDMap             map[int]int        // mapping from core to RMID
	PackageThrottled    map[int]bool       // if a physical numa node is MB throttled
	NumaLowPriThrottled map[int]bool       // if the low priority instances on a physical numa has been throttled
	NumaThrottled       map[int]bool       // if a numa is MB throttled
	Instances           []Instance
}

type CosEntry struct {
	Used    bool   // if used by a instance
	Cap     uint64 // the memory-bandwidth upper-bound
	InsName string // the instance using this cos on a CCD
}

// computing instance for socket container spec
type Instance struct {
	Name        string
	Priority    int              // 1: high; 2: low
	Request     uint64           // min memory bandwidth
	Limit       uint64           // max memory bandwidth
	SoftLimit   bool             // throttle the high-priority instances based on their real-time throughput if all have the same priority
	Nodes       map[int]struct{} // the Numa nodes where it is running on
	CosTracking map[int]int      // track the cos usage on each CCD it is running on
}

func (c MBController) GetInstancesByCCD(ccd int) []Instance {
	instances := make([]Instance, 0)
	for _, ins := range c.Instances {
		if _, ok := ins.CosTracking[ccd]; ok {
			instances = append(instances, ins)
		}
	}

	return instances
}
