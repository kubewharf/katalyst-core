package vci

import (
	"encoding/json"
	"fmt"
	"strconv"

	"k8s.io/api/core/v1"
	"k8s.io/klog/v2"
)

const (
	// VciPodResourceKey vci pod level resource annotation,
	// including cpu/memory/hugepages/device info and their counterpart numa topology details.
	VciPodResourceKey = "vci.volcengine.com/pod-resource-spec"

	// VciPodCPUStaticPolicyOptions specify the options to fine-tune the cpumanager policies.
	VciPodCPUStaticPolicyOptions = "vci.volcengine.com/cpu-static-policy-options"
)

var (
	defaultHugePagesResourceName = v1.ResourceName(fmt.Sprintf("%s%s", v1.ResourceHugePagesPrefix, "2Mi"))
)

const (
	// CPUQuotaUnit specify cpu quota per numa node
	CPUQuotaUnit = "cpu-quota"
	// CPUSetUnit specify cpu thead ids per numa node
	CPUSetUnit = "cpu-set"
	// DPUVFPrefix is the prefix of dpu vf device
	DPUVFPrefix = "dpu.volcengine.com/"
	// NvidiaGPUPrefix is the prefix of nvidia gpu device
	NvidiaGPUPrefix = "nvidia.com"
)

// ExtendResourceInfo indicates the general resource info
type ExtendResourceInfo struct {
	Resource string `json:"resource,omitempty"`
	Limit    string `json:"limit,omitempty"`
	Node     string `json:"node,omitempty"`
}

// PodResourceSpec specify resource limits/requests and topology for VCI pod,  the value of Resources.Limits cannot be empty.
// If Resources.Limits are empty, indicates that it is not VCI Pod and without effective pod level resource specified.
type PodResourceSpec struct {
	MemoryInfo []ExtendResourceInfo `json:"memoryInfo,omitempty"`
	CPUInfo    []ExtendResourceInfo `json:"cpuInfo,omitempty"`
	DeviceInfo []ExtendResourceInfo `json:"deviceInfo,omitempty"`
	// Resources.Limits are equal to Resources.Requests by default.
	Resources v1.ResourceRequirements `json:"resources,omitempty"`
}

// GetPodResourceSpec get PodResourceSpec from annotations of pod. ResourceRequirements.Limits cannot be empty
func GetPodResourceSpec(annotations map[string]string) *PodResourceSpec {
	if annotations == nil {
		return nil
	}

	spec := PodResourceSpec{}
	if s, find := annotations[VciPodResourceKey]; find {
		if err := json.Unmarshal([]byte(s), &spec); err != nil {
			klog.Errorf("Parse annotation with key:%v err:%v", VciPodResourceKey, err)
			return nil
		}

		return &spec
	}

	return nil
}

func GetCPUNUMASpec(cpuInfo []ExtendResourceInfo, cpuNumLimits int, anno map[string]string) (numaToCPUNum map[int]int, policyOpts string, err error) {
	numaAlign := make(map[int]int)
	cpuTotalFromNUMASpec := 0
	var policyOption string

	for _, cInfo := range cpuInfo {
		if cInfo.Resource == CPUQuotaUnit {
			numaID, err := strconv.Atoi(cInfo.Node)
			if err != nil {
				return nil, policyOption, err
			}

			cpuNum, err := strconv.Atoi(cInfo.Limit)
			if err != nil {
				return nil, policyOption, err
			}
			numaAlign[numaID] = cpuNum
			cpuTotalFromNUMASpec += cpuNum
		}
	}

	if len(numaAlign) != 0 && cpuTotalFromNUMASpec != cpuNumLimits {
		return nil, policyOption, fmt.Errorf("[cpumanager] cpu shares:%v from limits are not equal to numa specified:%v", cpuNumLimits, cpuTotalFromNUMASpec)
	}

	// Just used for russ
	if s, find := anno[VciPodCPUStaticPolicyOptions]; find && len(numaAlign) != 0 {
		policyOption = s
	}

	return numaAlign, policyOption, nil
}
