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
	"strings"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/writemb"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/writemb/l3pmc"
)

type MBMonitor interface {
	GetMBQoSGroups() (map[task.QoSGroup]*MBQoSGroup, error)
}

func newMBMonitor(taskManager task.Manager, rmbReader task.TaskMBReader, wmbReader writemb.WriteMBReader) (MBMonitor, error) {
	return &mbMonitor{
		taskManager: taskManager,
		rmbReader:   rmbReader,
		wmbReader:   wmbReader,
	}, nil
}

func NewDefaultMBMonitor(dieCPUs map[int][]int, dataKeeper state.MBRawDataKeeper, taskManager task.Manager, domainManager *mbdomain.MBDomainManager) (MBMonitor, error) {
	taskMBReader, err := task.CreateTaskMBReader(dataKeeper)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create task mb reader")
	}

	wmbReader, err := l3pmc.NewWriteMBReader(dieCPUs)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create writes mb reader")
	}
	podMBMonitor, err := newMBMonitor(taskManager, taskMBReader, wmbReader)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create pod mb monitor")
	}

	return podMBMonitor, nil
}

type mbMonitor struct {
	taskManager task.Manager
	rmbReader   task.TaskMBReader
	wmbReader   writemb.WriteMBReader
}

func (m mbMonitor) GetMBQoSGroups() (map[task.QoSGroup]*MBQoSGroup, error) {
	if err := m.refreshTasks(); err != nil {
		return nil, errors.Wrap(err, "failed to refresh task")
	}

	rQoSCCDMB, err := m.getReadsMBs()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get reads mb")
	}

	wQoSCCDMB, err := m.getWritesMBs(getCCDQoSGroups(rQoSCCDMB))
	if err != nil {
		return nil, errors.Wrap(err, "failed to get writes mb")
	}

	groupCCDMBs := getGroupCCDMBs(rQoSCCDMB, wQoSCCDMB)
	groups := make(map[task.QoSGroup]*MBQoSGroup)
	for qos, ccdMB := range groupCCDMBs {
		groups[qos] = newMBQoSGroup(ccdMB)
	}

	return groups, nil
}

func getGroupCCDMBs(rGroupCCDMB, wGroupCCDMB map[task.QoSGroup]map[int]int) map[task.QoSGroup]map[int]*MBData {
	// precondition: rGroupCCDMB, wGroupCCDMB have identical keys of qos group
	groupCCDMBs := make(map[task.QoSGroup]map[int]*MBData)
	for qos, ccdMB := range rGroupCCDMB {
		groupCCDMBs[qos] = make(map[int]*MBData)
		for ccd, mb := range ccdMB {
			groupCCDMBs[qos][ccd] = &MBData{ReadsMB: mb}
		}
	}
	for qos, ccdMB := range wGroupCCDMB {
		for ccd, mb := range ccdMB {
			groupCCDMBs[qos][ccd].WritesMB = mb
		}
	}

	return groupCCDMBs
}

func getCCDQoSGroups(qosMBs map[task.QoSGroup]map[int]int) map[int][]task.QoSGroup {
	result := make(map[int][]task.QoSGroup)
	for qos, ccdmb := range qosMBs {
		for ccd, _ := range ccdmb {
			result[ccd] = append(result[ccd], qos)
		}
	}
	return result
}

func (m mbMonitor) getReadsMBs() (map[task.QoSGroup]map[int]int, error) {
	result := make(map[task.QoSGroup]map[int]int)

	// todo: read in parallel to speed up
	for _, pod := range m.taskManager.GetTasks() {
		ccdMB, err := m.rmbReader.GetMB(pod)
		if err != nil {
			if errors.Is(err, resctrl.ErrUninitialized) {
				continue
			}
			return nil, errors.Wrap(err, fmt.Sprintf("failed to get mb of pod %s", pod.PodUID))
		}

		if _, ok := result[pod.QoSGroup]; !ok {
			result[pod.QoSGroup] = make(map[int]int)
		}
		for ccd, mb := range ccdMB {
			result[pod.QoSGroup][ccd] += mb
		}
	}

	return result, nil
}

func (m mbMonitor) getWritesMBs(ccdQoSGroup map[int][]task.QoSGroup) (map[task.QoSGroup]map[int]int, error) {
	result := make(map[task.QoSGroup]map[int]int)
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

func (m mbMonitor) refreshTasks() error {
	return m.taskManager.RefreshTasks()
}

func DisplayMBSummary(qosCCDMB map[task.QoSGroup]*MBQoSGroup) string {
	var sb strings.Builder
	sb.WriteString("----- mb summary -----\n")
	for qos, ccdmb := range qosCCDMB {
		sb.WriteString(fmt.Sprintf("--QoS: %s\n", qos))
		for ccd, mb := range ccdmb.CCDMB {
			sb.WriteString(fmt.Sprintf("      ccd %d: r %d, w %d\n", ccd, mb.ReadsMB, mb.WritesMB))
		}
	}
	return sb.String()
}
