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

package dynamicpolicy

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/atomic"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/external/rdt"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	resctrlutil "github.com/kubewharf/katalyst-core/pkg/util/resctrl"
)

const (
	metricNameResctrlMonGroupsNum       = "resctrl_mon_groups_num"
	metricNameResctrlMonGroupsOverlimit = "resctrl_mon_groups_over_limit"
)

type ResctrlHinter interface {
	Run(stopCh <-chan struct{})
	HintResourceAllocation(podMeta commonstate.AllocationMeta, resourceAllocation *pluginapi.ResourceAllocation)
	Allocate(podMeta commonstate.AllocationMeta, resourceAllocation *pluginapi.ResourceAllocation)
}

type resctrlHinter struct {
	emitter              metrics.MetricEmitter
	config               *qrmresctrl.ResctrlConfig
	closidEnablingGroups sets.String
	enabledQoS           sets.String
	monGroupsMaxCount    *atomic.Int64
	root                 string
	// manager handles resctrl filesystem operations (creation/cleanup),
	// logic migrated from kubelet resctrlmanager.
	manager     resctrl.Manager
	state       state.ReadonlyState
	dynamicConf *dynamicconfig.DynamicAgentConfiguration
}

func injectRespAnnotation(resourceAllocation *pluginapi.ResourceAllocation, k, v string) {
	if _, ok := resourceAllocation.ResourceAllocation[string(v1.ResourceMemory)]; !ok {
		resourceAllocation.ResourceAllocation[string(v1.ResourceMemory)] = &pluginapi.ResourceAllocationInfo{}
	}

	allocInfo := resourceAllocation.ResourceAllocation[string(v1.ResourceMemory)]
	if allocInfo.Annotations == nil {
		allocInfo.Annotations = make(map[string]string)
	}

	allocInfo.Annotations[k] = v
}

func isPodLevelSubgroupDisabled(group string, enablingGroups sets.String) bool {
	// by default no special mon_groups layout, which allows kubelet to decide by itself, so not to explicitly disable
	if len(enablingGroups) == 0 {
		return false
	}

	return !enablingGroups.Has(group)
}

func (r *resctrlHinter) hintResourceAllocation(podMeta commonstate.AllocationMeta, resourceAllocation *pluginapi.ResourceAllocation, isAllocate bool) {
	if r.config == nil || resourceAllocation == nil || !r.config.EnableResctrlHint {
		return
	}
	if r.isRDTDisabled() {
		// An empty CLOS ID is an explicit suppression signal for kubelet-side
		// default CLOS fallback.
		if r.enabledQoS.Has(podMeta.QoSLevel) {
			injectRespAnnotation(resourceAllocation, util.AnnotationRdtClosID, "")
		}
		return
	}

	resctrlGroup, err := resctrlutil.ResolvePoolClosID(podMeta.ToClosAssignmentMeta(), r.config)
	if err != nil {
		general.Errorf("pod admit: fail to identify resctrl top level group for qos %s; skip resctl hint", podMeta.QoSLevel)
		return
	}

	// inject pod mon group (false only) if applicable
	needMonGroups := true
	if isPodLevelSubgroupDisabled(resctrlGroup, r.closidEnablingGroups) {
		general.InfofV(6, "mbm: pod %s/%s of group %s has no pod level mon subgroups",
			podMeta.PodNamespace, podMeta.PodName, resctrlGroup)
		needMonGroups = false

	} else if isAllocate && r.isMonGroupsOverLimit() {
		general.Infof("mbm: pod %s/%s mon_groups count has over limit %d, don't create pod level mon group",
			podMeta.PodNamespace, podMeta.PodName, r.monGroupsMaxCount.Load())
		needMonGroups = false
	}

	if err := r.manager.Create(podMeta.PodUid, resctrlGroup, isAllocate && needMonGroups); err != nil {
		general.Errorf("mbm: failed to confirm resctrl group for pod %s/%s: %v", podMeta.PodNamespace, podMeta.PodName, err)
		return
	}

	// Hint and Allocate must establish the CLOS before publishing it to kubelet. In
	// particular, a DisableRDT true->false transition may still be pending in
	// the manager even though the latest dynamic configuration is enabled.
	if r.enabledQoS.Has(podMeta.QoSLevel) {
		injectRespAnnotation(resourceAllocation, util.AnnotationRdtClosID, resctrlGroup)
	}

	if !needMonGroups {
		injectRespAnnotation(resourceAllocation, util.AnnotationRdtNeedPodMonGroups, strconv.FormatBool(needMonGroups))
	}

	return
}

func (r *resctrlHinter) isRDTDisabled() bool {
	if r == nil || r.dynamicConf == nil {
		return false
	}
	dynamicConfiguration := r.dynamicConf.GetDynamicConfiguration()
	return dynamicConfiguration != nil &&
		dynamicConfiguration.QRMPluginConfiguration != nil &&
		dynamicConfiguration.RDTConfig.DisableRDT
}

func (r *resctrlHinter) getMonGroupsMaxCount() int64 {
	if r.config == nil || r.config.MonGroupMaxCountRatio <= 0 {
		return 0
	}

	b, err := os.ReadFile(filepath.Join(r.root, "info/L3_MON/num_rmids"))
	if err != nil {
		general.Errorf("mbm: max mon_groups count: read num_rmids error: %v", err)
		return 0
	}
	limitStr := string(bytes.TrimSpace(b))
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		general.Errorf("mbm: max mon_groups count: parse num_rmids %s error: %v", limitStr, err)
		return 0
	}
	limit = int(float64(limit) * r.config.MonGroupMaxCountRatio)
	general.Infof("mbm: max mon_groups count: %d, ratio: %.2f", limit, r.config.MonGroupMaxCountRatio)
	return int64(limit)
}

func (r *resctrlHinter) isMonGroupsOverLimit() bool {
	var (
		monGroupsMaxCount int64 = r.monGroupsMaxCount.Load()
		monGroupsCount    int64
		err               error
	)

	if monGroupsMaxCount == 0 {
		return false
	}

	monGroupsCount, err = r.manager.GetMonGroupsCount()
	if err != nil {
		general.Errorf("mbm: check mon_groups: get mon groups count error: %v", err)
		return false
	}

	_ = r.emitter.StoreInt64(metricNameResctrlMonGroupsNum, monGroupsCount, metrics.MetricTypeNameRaw)
	if monGroupsCount >= monGroupsMaxCount {
		_ = r.emitter.StoreInt64(metricNameResctrlMonGroupsOverlimit, 1, metrics.MetricTypeNameRaw)
		return true
	}
	return false
}

func (r *resctrlHinter) HintResourceAllocation(podMeta commonstate.AllocationMeta, resourceAllocation *pluginapi.ResourceAllocation) {
	r.hintResourceAllocation(podMeta, resourceAllocation, false)
}

func (r *resctrlHinter) Allocate(podMeta commonstate.AllocationMeta, resourceAllocation *pluginapi.ResourceAllocation) {
	r.hintResourceAllocation(podMeta, resourceAllocation, true)
}

func (r *resctrlHinter) Run(stopCh <-chan struct{}) {
	go r.manager.Run(stopCh)
	go wait.Until(func() {
		entries := r.state.GetPodResourceEntries()
		uids := entries.GetAllPodUIDs()
		r.reconcileClos(uids)
	}, time.Minute, stopCh)

	wait.Until(func() {
		if count := r.getMonGroupsMaxCount(); count != r.monGroupsMaxCount.Load() {
			r.monGroupsMaxCount.Store(count)
		}
	}, 10*time.Minute, stopCh)
}

func (r *resctrlHinter) reconcileClos(activePodUIDs sets.String) {
	if err := r.manager.ReconcileClos(resctrl.ClosReconcileState{
		DisableRDT:      r.isRDTDisabled(),
		ExpectedClosIDs: r.expectedClosIDs(),
		ActivePodUIDs:   activePodUIDs,
	}); err != nil {
		general.Errorf("resctrl: reconcile clos failed: %v", err)
	}
}

func (r *resctrlHinter) expectedClosIDs() sets.String {
	expected := sets.NewString()
	if r == nil || r.state == nil || r.config == nil {
		return expected
	}
	metas := make([]resctrlutil.ClosAssignmentMeta, 0)
	for _, podEntries := range r.state.GetPodResourceEntries() {
		for _, containerEntries := range podEntries {
			for _, allocationInfo := range containerEntries {
				if allocationInfo == nil {
					continue
				}
				metas = append(metas, allocationInfo.AllocationMeta.ToClosAssignmentMeta())
			}
		}
	}
	poolsByClos, err := resctrlutil.BuildExpectedClosPools(metas, r.config)
	if err != nil {
		general.Errorf("resctrl: build expected CLOS IDs failed: %v", err)
		return expected
	}
	for closID := range poolsByClos {
		expected.Insert(closID)
	}
	return expected
}

func newResctrlHinter(config *qrmresctrl.ResctrlConfig, dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, state state.ReadonlyState,
) ResctrlHinter {
	r := &resctrlHinter{
		emitter:     emitter,
		config:      config,
		root:        consts.DefaultResctrlRootDir,
		manager:     resctrl.NewManager(config, rdt.NewDefaultManager()),
		state:       state,
		dynamicConf: dynamicConf,
	}

	if config != nil {
		r.closidEnablingGroups = sets.NewString(config.MonGroupEnabledClosIDs...)
		r.enabledQoS = sets.NewString(config.EnabledQoS...)
	}
	r.monGroupsMaxCount = atomic.NewInt64(r.getMonGroupsMaxCount())
	return r
}
