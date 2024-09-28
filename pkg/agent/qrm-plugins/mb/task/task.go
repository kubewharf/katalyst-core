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

package task

import (
	"fmt"
	"path"
	"sort"

	"k8s.io/apimachinery/pkg/util/sets"

	resctrlconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
)

type Task struct {
	QoSGroup QoSGroup

	// PodUID is the pod identifier that leverage host cgroup to locate the cpu/ccd/numa node info
	// including pod prefix and uid string, like "poda47c5c03-cf94-4a36-b52f-c1cb17dc1675"
	PodUID string

	NumaNodes []int // NumaNodes are the set of numa nodes associated to the task via its bound CCDs
	CCDs      []int // CCDs are the set of CCDs bound to the task via CPUs the task is running on
	CPUs      []int // CPUs are the set of cpus that task is running on
}

func (t Task) GetID() string {
	return t.PodUID
}

func GetResctrlCtrlGroupFolder(qos QoSGroup) (string, error) {
	return path.Join(resctrlconsts.FsRoot, string(qos)), nil
}

func (t Task) GetResctrlCtrlGroup() (string, error) {
	return GetResctrlCtrlGroupFolder(t.QoSGroup)
}

func (t Task) GetResctrlMonGroup() (string, error) {
	taskCtrlGroup, err := t.GetResctrlCtrlGroup()
	if err != nil {
		return "", err
	}

	taskFolder := fmt.Sprintf(resctrlconsts.TmplTaskFolder, t.PodUID)
	return path.Join(taskCtrlGroup, resctrlconsts.SubGroupMonRoot, taskFolder), nil
}

func getCCDs(cpus []int, cpuCCD map[int]int) ([]int, error) {
	ccds := make(sets.Int)
	for _, cpu := range cpus {
		ccd, ok := cpuCCD[cpu]
		if !ok {
			return nil, fmt.Errorf("cpu %d has no ccd", cpu)
		}

		ccds.Insert(ccd)
	}

	result := make(sort.IntSlice, len(ccds))
	i := 0
	for ccd, _ := range ccds {
		result[i] = ccd
		i++
	}
	result.Sort()
	return result, nil
}

func getCgroupCPUSetPath(podUID string, qosGroup QoSGroup) (string, error) {
	// todo: support cgroup v2
	// below assumes cgroup v1
	qos, err := NewQoS(qosGroup)
	if err != nil {
		return "", err
	}
	return path.Join("/sys/fs/cgroup/cpuset/kubepods/", qosLevelToCgroupv1GroupFolder[qos.Level], podUID), nil
}
