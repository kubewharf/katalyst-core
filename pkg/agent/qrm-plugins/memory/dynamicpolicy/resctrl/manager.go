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

package resctrl

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.uber.org/atomic"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	PodDirPrefix = "pod"
	MonGroupsDir = "mon_groups"
	tasks        = "tasks"
	cpus         = "cpus"
	schemata     = "schemata"
)

type Manager interface {
	Run(stopCh <-chan struct{})
	Create(podUID, closID string, createMonGroup bool) error
	Cleanup(activePodUIDs sets.String) error
	GetMonGroupsCount() (int64, error)
}

type managerImpl struct {
	config  *qrm.ResctrlConfig
	enabled atomic.Bool
	root    string
	sync.RWMutex
}

func NewManager(config *qrm.ResctrlConfig) Manager {
	return &managerImpl{
		config: config,
	}
}

func (m *managerImpl) Run(stopCh <-chan struct{}) {
	if m.config != nil && !m.config.EnableResctrlGroupLifecycleManagement {
		return
	}
	wait.Until(func() {
		root, err := findResctrlMountpointDir()
		enable := root != ""
		if m.enabled.Load() == enable && m.root == root {
			return
		}
		m.Lock()
		m.root = root
		m.Unlock()
		m.enabled.Store(enable)
		general.Infof("resctrl enabled %v: root %s, error: %v", enable, root, err)
	}, time.Minute, stopCh)
}

func (m *managerImpl) Create(podUID, closID string, createMonGroup bool) error {
	if m.config != nil && !m.config.EnableResctrlGroupLifecycleManagement {
		general.Infof("resctrl group lifecycle management disabled, skip creating pod %s closID %s", podUID, closID)
		return nil
	}
	if !m.enabled.Load() {
		return nil
	}

	m.RLock()
	root := m.root
	m.RUnlock()

	closIDPath := filepath.Join(root, closID)
	// create closid dir
	if err := os.MkdirAll(closIDPath, 0o755); err != nil {
		return fmt.Errorf("create clos_id dir %s failed: %v", closIDPath, err)
	}

	if createMonGroup {
		rmID := PodDirPrefix + podUID
		monGroupsPath := filepath.Join(closIDPath, MonGroupsDir, rmID)
		if err := os.MkdirAll(monGroupsPath, 0o755); err != nil {
			return fmt.Errorf("create mon_groups dir %s failed: %v", monGroupsPath, err)
		}
	}
	return nil
}

func (m *managerImpl) Cleanup(activePodUIDs sets.String) error {
	if m.config != nil && !m.config.EnableResctrlGroupLifecycleManagement {
		return nil
	}
	if !m.enabled.Load() {
		return nil
	}

	m.RLock()
	root := m.root
	m.RUnlock()

	walkMonGroupsDirs(root, func(uid, closID, path string) {
		if activePodUIDs.Has(uid) || !isTasksEmpty(path) {
			return
		}
		general.Infof("resctrl: remove pod %s mon_groups dir %s", uid, path)
		if err := os.RemoveAll(path); err != nil {
			general.Errorf("resctrl: remove pod %s mon_groups dir %s error: %v", uid, path, err)
		}
	}, func(closID, path string) {
		if !isTasksEmpty(path) {
			return
		}
		monGroupPath := filepath.Join(path, MonGroupsDir)
		if entries, err := os.ReadDir(monGroupPath); err == nil && len(entries) > 0 {
			return
		}
		general.Infof("resctrl: remove clos_id dir %s", path)
		if err := os.RemoveAll(path); err != nil {
			general.Errorf("resctrl: remove clos_id dir %s error: %v", path, err)
		}
	})
	return nil
}

func (m *managerImpl) GetMonGroupsCount() (int64, error) {
	if !m.enabled.Load() {
		return 0, nil
	}

	m.RLock()
	root := m.root
	m.RUnlock()

	var count int64
	subdirs, err := os.ReadDir(root)
	if err != nil {
		return 0, fmt.Errorf("read root %s error: %v", root, err)
	}
	for _, subdir := range subdirs {
		if !subdir.IsDir() || subdir.Name() == "info" || subdir.Name() == "mon_data" || subdir.Name() == MonGroupsDir {
			continue
		}
		monGroupPath := filepath.Join(root, subdir.Name(), MonGroupsDir)
		monGroupsDirs, err := os.ReadDir(monGroupPath)
		if err != nil && !os.IsNotExist(err) {
			general.Errorf("resctrl: read mon_groups dir %s error: %v", monGroupPath, err)
			continue
		}
		count += int64(len(monGroupsDirs))
	}
	return count, nil
}

func isTasksEmpty(root string) bool {
	path := filepath.Join(root, tasks)
	f, err := os.Stat(path)
	if err != nil {
		return true
	}
	return f.Size() == 0
}

func walkMonGroupsDirs(root string, walkMonGroupsFunc func(uid, closID, path string), walkClosIDFunc func(closID, path string)) {
	subdirs, err := os.ReadDir(root)
	if err != nil {
		general.Errorf("resctrl: read root %s error: %v", root, err)
		return
	}
	for _, subdir := range subdirs {
		if !subdir.IsDir() {
			continue
		}
		if sets.NewString("info", "mon_data", MonGroupsDir).Has(subdir.Name()) {
			continue
		}
		closID := subdir.Name()
		monGroupPath := filepath.Join(root, closID, MonGroupsDir)

		monGroupsSubdirs, err := os.ReadDir(monGroupPath)
		if err != nil {
			if !os.IsNotExist(err) {
				general.Errorf("resctrl: read mon_groups dir %s error: %v", monGroupPath, err)
			}
		} else {
			wg := sync.WaitGroup{}
			for _, monGroupsSubdir := range monGroupsSubdirs {
				wg.Add(1)
				go func(d fs.DirEntry) {
					defer func() {
						wg.Done()
						if r := recover(); r != nil {
							general.Errorf("resctrl: walk mon_groups dir panic: %v", r)
						}
					}()

					rmID := d.Name()
					if !d.IsDir() || !strings.HasPrefix(rmID, PodDirPrefix) {
						return
					}
					podMonGroupPath := filepath.Join(monGroupPath, rmID)
					uid := strings.TrimPrefix(rmID, PodDirPrefix)

					if walkMonGroupsFunc != nil {
						walkMonGroupsFunc(uid, closID, podMonGroupPath)
					}
				}(monGroupsSubdir)
			}
			wg.Wait()
		}

		if walkClosIDFunc != nil {
			walkClosIDFunc(closID, filepath.Join(root, closID))
		}
	}
}
