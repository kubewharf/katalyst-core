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

package l3pmc

import (
	"fmt"
	"time"

	"github.com/pkg/errors"

	// todo: this is internal repo - move code into out-of-tree qrm plugin of the internal adapter
	amdutilpkg "code.byted.org/tce/amd-utils/pkg"
	"code.byted.org/tce/amd-utils/pkg/msr"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/writemb"
)

type writeMBReader struct {
	ccdMonitors map[int]msr.Monitor
}

func (w writeMBReader) GetMB(ccd int) (int, error) {
	val, err := w.ccdMonitors[ccd].Get()
	return int(val), err
}

func NewWriteMBReader(ccdCPUs map[int][]int) (writemb.WriteMBReader, error) {
	op, _ := amdutilpkg.NewOperation()
	ccdMonitors := make(map[int]msr.Monitor)
	for ccd, cpus := range ccdCPUs {
		if len(cpus) == 0 {
			return nil, fmt.Errorf("invalid ccd-cpu topology, ccd %d has no cpu", ccd)
		}

		// each ccd suffices to pick 1 cpu as readings from all its cpus are identical
		cpu := cpus[0]
		// we choose to use L3PMCCRX_5 here as L3PMCCRX_0 to L3PMCCRX_3 are reserved by others
		monitor, err := msr.NewL3PMCVictimMonitor(&op, uint32(cpu), amdutilpkg.L3PMCCTL_5, amdutilpkg.L3PMCCTR_5, time.Second*1)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create monitor of l3pmc victim")
		}
		// todo: put start in Start method enforcing Start/Stop semantic
		if err := monitor.Start(); err != nil {
			return nil, errors.Wrap(err, "failed to start monitor of l3pmc victim")
		}
		ccdMonitors[ccd] = monitor
	}
	return &writeMBReader{
		ccdMonitors: ccdMonitors,
	}, nil
}
