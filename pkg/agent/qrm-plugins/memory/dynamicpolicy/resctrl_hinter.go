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
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"go.uber.org/atomic"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	templateSharedSubgroup = "shared-%02d"
	sharedGroup            = "share"
	resctrlRoot            = "/sys/fs/resctrl"
	monGroups              = "mon_groups"

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
	config               *qrm.ResctrlConfig
	closidEnablingGroups sets.String
	monGroupsMaxCount    *atomic.Int64
	root                 string
}

func getSharedSubgroup(val int) string {
	// typical mon group is like "shared-xx", except for
	// negative value indicates using "shared" mon group
	if val < 0 {
		return sharedGroup
	}
	return fmt.Sprintf(templateSharedSubgroup, val)
}

func (r *resctrlHinter) getSharedSubgroupByPool(pool string) string {
	if v, ok := r.config.CPUSetPoolToSharedSubgroup[pool]; ok {
		return getSharedSubgroup(v)
	}
	return getSharedSubgroup(r.config.DefaultSharedSubgroup)
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

	var resctrlGroup string
	switch podMeta.QoSLevel {
	case apiconsts.PodAnnotationQoSLevelSystemCores:
		// tweak the case of system qos
		resctrlGroup = commonstate.PoolNamePrefixSystem
	case apiconsts.PodAnnotationQoSLevelSharedCores:
		resctrlGroup = r.getSharedSubgroupByPool(podMeta.OwnerPoolName)
	default:
		resctrlGroup = podMeta.OwnerPoolName
	}

	// when no recognized qos can be identified, no hint
	if resctrlGroup == commonstate.EmptyOwnerPoolName {
		general.Errorf("pod admit: fail to identify resctrl top level group for qos %s; skip resctl hint", podMeta.QoSLevel)
		return
	}

	// inject shared subgroup if share pool
	if podMeta.QoSLevel == apiconsts.PodAnnotationQoSLevelSharedCores {
		injectRespAnnotation(resourceAllocation, util.AnnotationRdtClosID, resctrlGroup)
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
	if !needMonGroups {
		injectRespAnnotation(resourceAllocation, util.AnnotationRdtNeedPodMonGroups, strconv.FormatBool(needMonGroups))
	}

	return
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
		monGroupsCount    int64 = 0
	)

	if monGroupsMaxCount == 0 {
		return false
	}

	subdirs, err := os.ReadDir(r.root)
	if err != nil {
		general.Errorf("mbm: check mon_groups: read root %s error: %v", r.root, err)
		return false
	}
	for _, subdir := range subdirs {
		if !subdir.IsDir() || subdir.Name() == "info" || subdir.Name() == "mon_data" || subdir.Name() == monGroups {
			continue
		}
		monGroupPath := filepath.Join(r.root, subdir.Name(), monGroups)
		monGroupsDirs, err := os.ReadDir(monGroupPath)
		if err != nil && !os.IsNotExist(err) {
			general.Errorf("mbm: check mon_groups: read mon_groups dir %s error: %v", monGroupPath, err)
			continue
		}
		monGroupsCount += int64(len(monGroupsDirs))
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
	wait.Until(func() {
		if count := r.getMonGroupsMaxCount(); count != r.monGroupsMaxCount.Load() {
			r.monGroupsMaxCount.Store(count)
		}
	}, 10*time.Minute, stopCh)
}

func newResctrlHinter(config *qrm.ResctrlConfig, emitter metrics.MetricEmitter) ResctrlHinter {
	closidEnablingGroups := make(sets.String)
	if config != nil && config.MonGroupEnabledClosIDs != nil {
		closidEnablingGroups = sets.NewString(config.MonGroupEnabledClosIDs...)
	}

	r := &resctrlHinter{
		emitter:              emitter,
		config:               config,
		closidEnablingGroups: closidEnablingGroups,
		root:                 resctrlRoot,
	}
	r.monGroupsMaxCount = atomic.NewInt64(r.getMonGroupsMaxCount())
	return r
}
