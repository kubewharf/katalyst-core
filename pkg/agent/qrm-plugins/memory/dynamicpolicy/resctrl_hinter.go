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
	"encoding/json"
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
	templateSharedSubgroup = "share-%02d"
	sharedGroup            = "share"
)

type ResctrlHinter interface {
	HintResp(qosLevel string, req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse,
	) *pluginapi.ResourceAllocationResponse
}

type resctrlHinter struct {
	config               *qrm.ResctrlConfig
	closidEnablingGroups sets.String
}

func identifyCPUSetPool(annoInReq map[string]string) string {
	if pool, ok := annoInReq[apiconsts.PodAnnotationCPUEnhancementCPUSet]; ok {
		return pool
	}

	// fall back to original composite (not flattened) form
	enhancementValue, ok := annoInReq[apiconsts.PodAnnotationCPUEnhancementKey]
	if !ok {
		return ""
	}

	flattenedEnhancements := map[string]string{}
	err := json.Unmarshal([]byte(enhancementValue), &flattenedEnhancements)
	if err != nil {
		return ""
	}
	return identifyCPUSetPool(flattenedEnhancements)
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

func ensureToGetMemAllocInfo(resp *pluginapi.ResourceAllocationResponse) *pluginapi.ResourceAllocationInfo {
	if _, ok := resp.AllocationResult.ResourceAllocation[string(v1.ResourceMemory)]; !ok {
		resp.AllocationResult.ResourceAllocation[string(v1.ResourceMemory)] = &pluginapi.ResourceAllocationInfo{}
	}

	allocInfo := resp.AllocationResult.ResourceAllocation[string(v1.ResourceMemory)]
	if allocInfo.Annotations == nil {
		allocInfo.Annotations = make(map[string]string)
	}

	return allocInfo
}

func injectRespAnnotationSharedGroup(resp *pluginapi.ResourceAllocationResponse, group string) {
	allocInfo := ensureToGetMemAllocInfo(resp)
	allocInfo.Annotations[util.AnnotationRdtClosID] = group
}

func injectRespAnnotationPodMonGroup(resp *pluginapi.ResourceAllocationResponse,
	enablingGroups sets.String, group string,
) {
	// by default no special mon_groups layout, which allows kubelet to decide by itself, no need to hint explicitly
	if len(enablingGroups) == 0 {
		return
	}

	// if enabled, no need to hint as kubelet treats default as true
	if enablingGroups.Has(group) {
		return
	}

	allocInfo := ensureToGetMemAllocInfo(resp)
	general.InfofV(6, "mbm: pod %s/%s qos %s not need pod mon_groups",
		resp.PodNamespace, resp.PodName, group)
	allocInfo.Annotations[util.AnnotationRdtNeedPodMonGroups] = strconv.FormatBool(false)
}

func (r *resctrlHinter) HintResp(qosLevel string,
	req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse,
) *pluginapi.ResourceAllocationResponse {
	if r.config == nil || !r.config.EnableResctrlHint {
		return resp
	}

	poolName := commonstate.GetSpecifiedPoolName(qosLevel, commonstate.EmptyOwnerPoolName)
	// tweak the case of system qos
	if poolName == apiconsts.PodAnnotationQoSLevelSystemCores {
		poolName = commonstate.PoolNamePrefixSystem
	}
	// when no recognized qos can be identified, no hint
	if poolName == commonstate.EmptyOwnerPoolName {
		general.Errorf("pod admit: fail to identify short qos level for %s; skip resctl hint", qosLevel)
		return resp
	}

	// resctrl hint cares sub pool actually
	subPoolName := poolName
	if qosLevel == apiconsts.PodAnnotationQoSLevelSharedCores {
		cpusetPool := identifyCPUSetPool(req.Annotations)
		subPoolName = r.getSharedSubgroupByPool(cpusetPool)
	}

	// inject shared subgroup if share pool
	if qosLevel == apiconsts.PodAnnotationQoSLevelSharedCores {
		injectRespAnnotationSharedGroup(resp, subPoolName)
	}

	// inject pod mon group (false only) if applicable
	injectRespAnnotationPodMonGroup(resp, r.closidEnablingGroups, subPoolName)

	return resp
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
