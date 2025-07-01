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
	"fmt"
	"strconv"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	templateSharedSubgroup = "shared-%02d"
	sharedGroup            = "share"
)

type ResctrlHinter interface {
	HintResourceAllocation(podMeta commonstate.AllocationMeta, resourceAllocation *pluginapi.ResourceAllocation)
}

type resctrlHinter struct {
	config               *qrm.ResctrlConfig
	closidEnablingGroups sets.String
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

func ensureToGetMemAllocInfo(resourceAllocation *pluginapi.ResourceAllocation) *pluginapi.ResourceAllocationInfo {
	if _, ok := resourceAllocation.ResourceAllocation[string(v1.ResourceMemory)]; !ok {
		resourceAllocation.ResourceAllocation[string(v1.ResourceMemory)] = &pluginapi.ResourceAllocationInfo{}
	}

	allocInfo := resourceAllocation.ResourceAllocation[string(v1.ResourceMemory)]
	if allocInfo.Annotations == nil {
		allocInfo.Annotations = make(map[string]string)
	}

	return allocInfo
}

func injectRespAnnotationSharedGroup(resourceAllocation *pluginapi.ResourceAllocation, group string) {
	allocInfo := ensureToGetMemAllocInfo(resourceAllocation)
	allocInfo.Annotations[util.AnnotationRdtClosID] = group
}

func isPodLevelSubgroupDisabled(group string, enablingGroups sets.String) bool {
	// by default no special mon_groups layout, which allows kubelet to decide by itself, so not to explicitly disable
	if len(enablingGroups) == 0 {
		return false
	}

	return !enablingGroups.Has(group)
}

func injectRespAnnotationPodMonGroup(podMeta commonstate.AllocationMeta, resourceAllocation *pluginapi.ResourceAllocation,
	enablingGroups sets.String, group string,
) {
	if isPodLevelSubgroupDisabled(group, enablingGroups) {
		allocInfo := ensureToGetMemAllocInfo(resourceAllocation)
		general.InfofV(6, "mbm: pod %s/%s of group %s has no pod level mon subgroups",
			podMeta.PodNamespace, podMeta.PodName, group)
		allocInfo.Annotations[util.AnnotationRdtNeedPodMonGroups] = strconv.FormatBool(false)
		return
	}

	// by default, or having pod level subgroup, no need to hint explicitly
	return
}

func (r *resctrlHinter) HintResourceAllocation(podMeta commonstate.AllocationMeta, resourceAllocation *pluginapi.ResourceAllocation) {
	if r.config == nil || !r.config.EnableResctrlHint {
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
		injectRespAnnotationSharedGroup(resourceAllocation, resctrlGroup)
	}

	// inject pod mon group (false only) if applicable
	injectRespAnnotationPodMonGroup(podMeta, resourceAllocation, r.closidEnablingGroups, resctrlGroup)

	return
}

func newResctrlHinter(config *qrm.ResctrlConfig) ResctrlHinter {
	closidEnablingGroups := make(sets.String)
	if config != nil && config.MonGroupEnabledClosIDs != nil {
		closidEnablingGroups = sets.NewString(config.MonGroupEnabledClosIDs...)
	}

	return &resctrlHinter{
		config:               config,
		closidEnablingGroups: closidEnablingGroups,
	}
}
