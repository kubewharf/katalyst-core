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
	"fmt"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/readmb/rmbtype"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/readmb"
	resctrlfile "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/file"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/writemb"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/writemb/l3pmc"
)

var (
	// todo: use a better mechanism to pass needed data than exposing global var
	// global var used by resctrl mb provisioner for creation of domain manager
	IncubationInterval time.Duration
	DomainMBCapacity   int
)

var (
	onceDefaultMBMonitorInit sync.Once
	defaultMBMonitor         MBMonitor
)

type MBMonitor interface {
	GetMBQoSGroups() (map[qosgroup.QoSGroup]*MBQoSGroup, error)
}

func newMBMonitor(rmbReader readmb.ReadMBReader, wmbReader writemb.WriteMBReader, fs afero.Fs) (MBMonitor, error) {
	return &mbMonitor{
		rmbReader: rmbReader,
		wmbReader: wmbReader,
		fs:        fs,
	}, nil
}

func NewDefaultMBMonitor(dieCPUs map[int][]int, dataKeeper state.MBRawDataKeeper, domainManager *mbdomain.MBDomainManager) (MBMonitor, error) {
	var err error
	onceDefaultMBMonitorInit.Do(func() {
		defaultMBMonitor, err = newDefaultMBMonitor(dieCPUs, dataKeeper, domainManager)
	})
	return defaultMBMonitor, err
}

func newDefaultMBMonitor(dieCPUs map[int][]int, dataKeeper state.MBRawDataKeeper, domainManager *mbdomain.MBDomainManager) (MBMonitor, error) {
	ccds := make([]int, 0, len(dieCPUs))
	for die := range dieCPUs {
		ccds = append(ccds, die)
	}

	rmbMBReader, err := readmb.CreateQoSGroupMBReader(ccds, dataKeeper)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create read mb reader")
	}

	wmbReader, err := l3pmc.NewWriteMBReader(dieCPUs)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create writes mb reader")
	}
	podMBMonitor, err := newMBMonitor(rmbMBReader, wmbReader, afero.NewOsFs())
	if err != nil {
		return nil, errors.Wrap(err, "failed to create pod mb monitor")
	}

	return podMBMonitor, nil
}

type mbMonitor struct {
	wmbReader writemb.WriteMBReader
	rmbReader readmb.ReadMBReader

	// facility for unit test
	fs afero.Fs
}

func (m mbMonitor) GetMBQoSGroups() (map[qosgroup.QoSGroup]*MBQoSGroup, error) {
	rQoSCCDMB, err := m.getTopLevelReadsMBs()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get reads mb")
	}

	wQoSCCDMB, err := m.getWritesMBs(getCCDQoSGroups(rQoSCCDMB))
	if err != nil {
		return nil, errors.Wrap(err, "failed to get writes mb")
	}

	groupCCDMBs := getGroupCCDMBs(rQoSCCDMB, wQoSCCDMB)
	groups := make(map[qosgroup.QoSGroup]*MBQoSGroup)
	for qos, ccdMB := range groupCCDMBs {
		groups[qos] = newMBQoSGroup(ccdMB)
	}

	return groups, nil
}

func isRWRatioValid(r, w int) bool {
	// in general r/w ratio is at lease 1.0
	return r >= w
}

func getGroupCCDMBs(rGroupCCDMB map[qosgroup.QoSGroup]map[int]rmbtype.MBStat, wGroupCCDMB map[qosgroup.QoSGroup]map[int]int,
) map[qosgroup.QoSGroup]map[int]*MBData {
	groupCCDMBs := make(map[qosgroup.QoSGroup]map[int]*MBData)
	for qos, ccdMB := range rGroupCCDMB {
		groupCCDMBs[qos] = make(map[int]*MBData)
		for ccd, mb := range ccdMB {
			groupCCDMBs[qos][ccd] = &MBData{
				ReadsMB:      mb.Total,
				LocalReadsMB: mb.Local,
			}
		}
	}
	for qos, ccdMB := range wGroupCCDMB {
		for ccd, wmb := range ccdMB {
			if _, ok := groupCCDMBs[qos]; !ok {
				groupCCDMBs[qos] = make(map[int]*MBData)
			}
			if _, ok := groupCCDMBs[qos][ccd]; !ok {
				groupCCDMBs[qos][ccd] = &MBData{}
			}
			if !isRWRatioValid(groupCCDMBs[qos][ccd].ReadsMB, wmb) {
				continue
			}
			groupCCDMBs[qos][ccd].WritesMB = wmb
		}
	}

	return groupCCDMBs
}

func getCCDQoSGroups(qosMBs map[qosgroup.QoSGroup]map[int]rmbtype.MBStat) map[int][]qosgroup.QoSGroup {
	result := make(map[int][]qosgroup.QoSGroup)
	for qos, ccdmb := range qosMBs {
		for ccd, _ := range ccdmb {
			result[ccd] = append(result[ccd], qos)
		}
	}
	return result
}

// getTopLevelReadsMBs gets MB based on top level mon data
func (m *mbMonitor) getTopLevelReadsMBs() (map[qosgroup.QoSGroup]map[int]rmbtype.MBStat, error) {
	result := make(map[qosgroup.QoSGroup]map[int]rmbtype.MBStat)

	qosLevels, err := resctrlfile.GetResctrlCtrlGroups(m.fs)
	if err != nil {
		return nil, err
	}

	for _, qosLevel := range qosLevels {
		result[qosgroup.QoSGroup(qosLevel)], err = m.rmbReader.GetMB(qosLevel)
	}

	return result, nil
}

func (m *mbMonitor) getWritesMBs(ccdQoSGroup map[int][]qosgroup.QoSGroup) (map[qosgroup.QoSGroup]map[int]int, error) {
	result := make(map[qosgroup.QoSGroup]map[int]int)
	for ccd, groups := range ccdQoSGroup {
		mb, err := m.wmbReader.GetMB(ccd)
		if err != nil {
			return nil, err
		}
		// theoretically there may have more than one qos ctrl group binding to a specific ccd
		// for now it is fine to duplicate mb usages among them (as in POC shared-30 groups are exclusive)
		// todo: figure out proper distributions of mb among qos ctrl groups binding to given ccd
		for _, qos := range groups {
			if _, ok := result[qos]; !ok {
				result[qos] = make(map[int]int)
			}
			result[qos][ccd] = mb
		}
	}

	return result, nil
}

func distributeLocalRemote(r, w, readLocal int) (rLocal, rRemote, wLocal, wRemote int) {
	rLocal = readLocal
	rRemote = r - readLocal
	if rRemote < 0 {
		rRemote = 0
	}

	lRatio := 1.0
	if r > 0 && rLocal <= r {
		lRatio = float64(rLocal) / float64(r)
	}

	wLocal = int(float64(w) * lRatio)
	wRemote = w - wLocal
	return
}

func DisplayMBSummary(qosCCDMB map[qosgroup.QoSGroup]*MBQoSGroup) string {
	var sb strings.Builder
	sb.WriteString("----- mb summary -----\n")
	for qos, ccdmb := range qosCCDMB {
		sb.WriteString(fmt.Sprintf("--QoS: %s\n", qos))
		for ccd, mb := range ccdmb.CCDMB {
			rLocal, rRemote, wLocal, wRemote := distributeLocalRemote(mb.ReadsMB, mb.WritesMB, mb.LocalReadsMB)
			totalLocal := mb.LocalTotalMB
			totalRemote := mb.TotalMB - totalLocal
			sb.WriteString(fmt.Sprintf("      ccd %d: r %d, w %d, total %d, [ r: (local: %d, remote: %d), w: (local: %d, (remote: %d), sum:(local: %d, remote: %d) ]\n",
				ccd, mb.ReadsMB, mb.WritesMB, mb.TotalMB,
				rLocal, rRemote, wLocal, wRemote,
				totalLocal, totalRemote,
			))
		}
	}
	return sb.String()
}
