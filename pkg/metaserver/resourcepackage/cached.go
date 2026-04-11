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

package resourcepackage

import (
	"context"
	"sync"
	"time"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

const (
	syncResourcePackageUpdatePeriod = 30 * time.Second
)

type CachedResourcePackageManager struct {
	mux                sync.RWMutex
	resourcePackageMap resourcepackage.NUMAResourcePackageItems

	ResourcePackageManager
}

func (m *CachedResourcePackageManager) Run(stopCh <-chan struct{}) error {
	m.updateResourcePackageMap()
	go wait.Until(m.updateResourcePackageMap, syncResourcePackageUpdatePeriod, stopCh)
	return nil
}

func NewCachedResourcePackageManager(rpm ResourcePackageManager) *CachedResourcePackageManager {
	m := &CachedResourcePackageManager{
		ResourcePackageManager: rpm,
	}
	return m
}

func (m *CachedResourcePackageManager) NodeResourcePackages(_ context.Context) (resourcepackage.NUMAResourcePackageItems, error) {
	m.mux.RLock()
	defer m.mux.RUnlock()
	return m.resourcePackageMap, nil
}

func (m *CachedResourcePackageManager) updateResourcePackageMap() {
	// Get resource package information from meta server
	resourcePackageMap, err := m.ResourcePackageManager.NodeResourcePackages(context.Background())
	if err != nil {
		general.Errorf("NodeResourcePackages failed with error: %v", err)
		return
	}

	m.mux.Lock()
	defer m.mux.Unlock()
	if apiequality.Semantic.DeepEqual(resourcePackageMap, m.resourcePackageMap) {
		return
	}
	general.Infof("update resource package map from %+v to %+v", m.resourcePackageMap, resourcePackageMap)
	m.resourcePackageMap = resourcePackageMap
}
