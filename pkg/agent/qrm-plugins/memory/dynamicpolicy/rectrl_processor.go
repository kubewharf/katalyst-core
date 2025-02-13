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

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
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
	option *qrm.ResctrlOptions
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

func injectRespAnnotationSharedGroup(resp *pluginapi.ResourceAllocationResponse, monGroup string) {
	if _, ok := resp.AllocationResult.ResourceAllocation[string(v1.ResourceMemory)]; !ok {
		resp.AllocationResult.ResourceAllocation[string(v1.ResourceMemory)] = &pluginapi.ResourceAllocationInfo{}
	}

	allocInfo := resp.AllocationResult.ResourceAllocation[string(v1.ResourceMemory)]
	if allocInfo.Annotations == nil {
		allocInfo.Annotations = make(map[string]string)
	}
	allocInfo.Annotations["rdt.resources.beta.kubernetes.io/pod"] = monGroup
}

func (r *resctrlHinter) HintResp(qosLevel string,
	req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse,
) *pluginapi.ResourceAllocationResponse {
	if r.option == nil || !r.option.EnableResctrlHint {
		return resp
	}

	// inject shared subgroup if applicable
	if qosLevel == apiconsts.PodAnnotationQoSLevelSharedCores {
		cpusetPool := identifyCPUSetPool(req.Annotations)
		monGroup := r.getSharedSubgroupByPool(cpusetPool)
		injectRespAnnotationSharedGroup(resp, monGroup)
	}

	return resp
}

func newResctrlHinter(option *qrm.ResctrlOptions) ResctrlHinter {
	return &resctrlHinter{option: option}
}
