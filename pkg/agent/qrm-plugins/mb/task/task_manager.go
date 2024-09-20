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
	"errors"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
)

type Manager interface {
	GetTasks() []*Task
	NewTask(podID string, qos QoSLevel) *Task
	FindTask(id string) (*Task, error)
	DeleteTask(task *Task)
}

func New(nodeCCDs map[int]sets.Int, cleaner state.MBRawDataCleaner) (Manager, error) {
	return &manager{
		rawStateCleaner: cleaner,
		nodeCCDs:        nodeCCDs,
	}, nil
}

type manager struct {
	rawStateCleaner state.MBRawDataCleaner
	nodeCCDs        map[int]sets.Int

	rwLock  sync.RWMutex
	tasks   map[string]*Task
	taskQoS map[string]QoSLevel
}

func (m *manager) DeleteTask(task *Task) {
	monGroup, _ := task.GetResctrlMonGroup()
	m.rawStateCleaner.Cleanup(monGroup)

	m.rwLock.Lock()
	m.rwLock.Unlock()
	delete(m.taskQoS, task.GetID())
	delete(m.tasks, task.GetID())
}

func (m *manager) FindTask(id string) (*Task, error) {
	m.rwLock.RLock()
	m.rwLock.RUnlock()
	task, ok := m.tasks[id]
	if !ok {
		return nil, errors.New("no task by the id")
	}

	return task, nil
}

func (m *manager) NewTask(podID string, qos QoSLevel) *Task {
	task := &Task{
		QoSLevel: qos,
		PodUID:   podID,
		nodeCCDs: m.nodeCCDs,
	}

	m.addTask(task)
	return task
}

func (m *manager) addTask(task *Task) {
	m.rwLock.Lock()
	defer m.rwLock.Unlock()
	m.taskQoS[task.GetID()] = task.QoSLevel
	m.tasks[task.GetID()] = task
}

func (m *manager) GetTasks() []*Task {
	m.rwLock.RLock()
	defer m.rwLock.RUnlock()

	result := make([]*Task, len(m.tasks))
	i := 0
	for _, task := range m.tasks {
		result[i] = task
		i++
	}

	return result
}
