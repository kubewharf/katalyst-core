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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	templateSharedSubgroup = "shared-%02d"
	sharedGroup            = "shared"
)

type ResctrlHinter interface {
	HintResp(qosLevel string, req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse,
	) *pluginapi.ResourceAllocationResponse
}

type resctrlHinter struct {
	option               *qrm.ResctrlOptions
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
	if v, ok := r.option.CPUSetPoolToSharedSubgroup[pool]; ok {
		return getSharedSubgroup(v)
	}
	return getSharedSubgroup(r.option.DefaultSharedSubgroup)
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
	if len(enablingGroups) == 0 || enablingGroups.Has(group) {
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
	if r.option == nil || !r.option.EnableResctrlHint {
		return resp
	}

	podShortQoS, ok := annoQoSLevelToShortQoSLevel[qosLevel]
	if !ok {
		general.Errorf("pod admit: fail to identify short qos level for %s; skip resctl hint", qosLevel)
		return resp
	}

	// inject shared subgroup if applicable
	if qosLevel == apiconsts.PodAnnotationQoSLevelSharedCores {
		cpusetPool := identifyCPUSetPool(req.Annotations)
		podShortQoS = r.getSharedSubgroupByPool(cpusetPool)
		injectRespAnnotationSharedGroup(resp, podShortQoS)
	}

	// inject pod mon group (false only) if applicable
	injectRespAnnotationPodMonGroup(resp, r.closidEnablingGroups, podShortQoS)

	return resp
}

func newResctrlHinter(option *qrm.ResctrlOptions) ResctrlHinter {
	closidEnablingGroups := make(sets.String)
	if option != nil && option.MonGroupsPolicy != nil {
		closidEnablingGroups = sets.NewString(option.MonGroupsPolicy.EnabledClosIDs...)
	}

	return &resctrlHinter{
		option:               option,
		closidEnablingGroups: closidEnablingGroups,
	}
}
