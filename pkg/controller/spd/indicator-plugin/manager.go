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

package indicator_plugin

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	indicatorSpecQueueLen   = 1000
	indicatorStatusQueueLen = 1000
)

// IndicatorUpdater is used by IndicatorPlugin as a unified implementation
// to trigger indicator updating logic.
type IndicatorUpdater interface {
	// UpdateBusinessIndicatorSpec + UpdateSystemIndicatorSpec + UpdateBusinessIndicatorStatus
	// for indicator add functions, IndicatorUpdater will try to merge them in local stores.
	UpdateBusinessIndicatorSpec(_ types.NamespacedName, _ []apiworkload.ServiceBusinessIndicatorSpec)
	UpdateSystemIndicatorSpec(_ types.NamespacedName, _ []apiworkload.ServiceSystemIndicatorSpec)
	UpdateBusinessIndicatorStatus(_ types.NamespacedName, _ []apiworkload.ServiceBusinessIndicatorStatus)
}

// IndicatorGetter is used by spd controller as indicator notifier to trigger
// update real spd.
type IndicatorGetter interface {
	// GetIndicatorSpecChan + GetIndicatorStatusChan
	// returns a channel to obtain the whether an update action has been triggered.
	GetIndicatorSpecChan() chan types.NamespacedName
	GetIndicatorStatusChan() chan types.NamespacedName

	// GetIndicatorSpec + GetIndicatorStatus
	// for indicator get functions, IndicatorUpdater will return a channel to obtain the merged results.
	GetIndicatorSpec(_ types.NamespacedName) *apiworkload.ServiceProfileDescriptorSpec
	GetIndicatorStatus(_ types.NamespacedName) *apiworkload.ServiceProfileDescriptorStatus
}

type IndicatorManager struct {
	specMtx   sync.Mutex
	specQueue chan types.NamespacedName
	specMap   map[types.NamespacedName]*apiworkload.ServiceProfileDescriptorSpec

	statusMtx   sync.Mutex
	statusQueue chan types.NamespacedName
	statusMap   map[types.NamespacedName]*apiworkload.ServiceProfileDescriptorStatus
}

var _ IndicatorUpdater = &IndicatorManager{}
var _ IndicatorGetter = &IndicatorManager{}

func NewIndicatorManager() *IndicatorManager {
	return &IndicatorManager{
		specQueue: make(chan types.NamespacedName, indicatorSpecQueueLen),
		specMap:   make(map[types.NamespacedName]*apiworkload.ServiceProfileDescriptorSpec),

		statusQueue: make(chan types.NamespacedName, indicatorStatusQueueLen),
		statusMap:   make(map[types.NamespacedName]*apiworkload.ServiceProfileDescriptorStatus),
	}
}

func (u *IndicatorManager) UpdateBusinessIndicatorSpec(nn types.NamespacedName, indicators []apiworkload.ServiceBusinessIndicatorSpec) {
	u.specMtx.Lock()

	insert := false
	if _, ok := u.specMap[nn]; !ok {
		insert = true
		u.specMap[nn] = initServiceProfileDescriptorSpec()
	}
	for _, indicator := range indicators {
		util.InsertSPDBusinessIndicatorSpec(u.specMap[nn], &indicator)
	}
	u.specMtx.Unlock()

	if insert {
		u.specQueue <- nn
	}
}

func (u *IndicatorManager) UpdateSystemIndicatorSpec(nn types.NamespacedName, indicators []apiworkload.ServiceSystemIndicatorSpec) {
	u.specMtx.Lock()

	insert := false
	if _, ok := u.specMap[nn]; !ok {
		insert = true
		u.specMap[nn] = initServiceProfileDescriptorSpec()
	}
	for _, indicator := range indicators {
		util.InsertSPDSystemIndicatorSpec(u.specMap[nn], &indicator)
	}
	u.specMtx.Unlock()

	if insert {
		u.specQueue <- nn
	}
}

func (u *IndicatorManager) UpdateBusinessIndicatorStatus(nn types.NamespacedName, indicators []apiworkload.ServiceBusinessIndicatorStatus) {
	u.statusMtx.Lock()

	insert := false
	if _, ok := u.statusMap[nn]; !ok {
		insert = true
		u.statusMap[nn] = initServiceProfileDescriptorStatus()
	}
	for _, indicator := range indicators {
		util.InsertSPDBusinessIndicatorStatus(u.statusMap[nn], &indicator)
	}

	u.statusMtx.Unlock()

	if insert {
		u.statusQueue <- nn
	}
}

func (u *IndicatorManager) GetIndicatorSpecChan() chan types.NamespacedName {
	return u.specQueue
}

func (u *IndicatorManager) GetIndicatorStatusChan() chan types.NamespacedName {
	return u.statusQueue
}

func (u *IndicatorManager) GetIndicatorSpec(nn types.NamespacedName) *apiworkload.ServiceProfileDescriptorSpec {
	u.specMtx.Lock()
	defer func() {
		delete(u.specMap, nn)
		u.specMtx.Unlock()
	}()

	spec, ok := u.specMap[nn]
	if !ok {
		klog.Warningf("spd spec doesn't exist for key: %s", nn.String())
		return nil
	}
	return spec
}

func (u *IndicatorManager) GetIndicatorStatus(nn types.NamespacedName) *apiworkload.ServiceProfileDescriptorStatus {
	u.statusMtx.Lock()
	defer func() {
		delete(u.statusMap, nn)
		u.statusMtx.Unlock()
	}()

	status, ok := u.statusMap[nn]
	if !ok {
		klog.Warningf("spd status doesn't exist for key: %s", nn.String())
		return nil
	}
	return status
}

func initServiceProfileDescriptorSpec() *apiworkload.ServiceProfileDescriptorSpec {
	return &apiworkload.ServiceProfileDescriptorSpec{
		BusinessIndicator: []apiworkload.ServiceBusinessIndicatorSpec{},
		SystemIndicator:   []apiworkload.ServiceSystemIndicatorSpec{},
	}
}

func initServiceProfileDescriptorStatus() *apiworkload.ServiceProfileDescriptorStatus {
	return &apiworkload.ServiceProfileDescriptorStatus{
		BusinessStatus: []apiworkload.ServiceBusinessIndicatorStatus{},
	}
}
