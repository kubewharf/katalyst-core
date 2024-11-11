package podadmit

import (
	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type PodAdmitter struct {
	nodePreempter *NodePreempter
	podSubgrouper *PodGrouper
}

func (p *PodAdmitter) PostProcessAllocate(req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse, qosLevel string, origReqAnno map[string]string,
) *pluginapi.ResourceAllocationResponse {
	general.InfofV(6, "mbm: resource allocate post process - pod %s/%s,  qos %v, anno %v", req.PodNamespace, req.PodName, qosLevel, origReqAnno)

	// to generalize high priority socket pod to dedicated_cores + numa binding + numa exclusive
	if IsDecdicatedCoresNumaExclusive(qosLevel, origReqAnno) {
		general.InfofV(6, "mbm: resource allocate post process - identified dedicated_cores numa exclusive pod %s/%s", req.PodNamespace, req.PodName)
		p.preemptNUMANodes(req)
	}

	if p.podSubgrouper.IsShared30(qosLevel, origReqAnno) {
		general.InfofV(6, "mbm: resource allocate post process - pod admitting %s/%s, shared-30", req.PodNamespace, req.PodName)
		p.hintRespWithShared30(resp)
	}
	return resp
}

func (p *PodAdmitter) preemptNUMANodes(req *pluginapi.ResourceRequest) {
	if err := p.nodePreempter.PreemptNodes(req); err != nil {
		general.Errorf("mbm: failed to preempt numa nodes for Socket pod %s/%s", req.PodNamespace, req.PodName)
	}
}

func (p *PodAdmitter) hintRespWithShared30(resp *pluginapi.ResourceAllocationResponse) *pluginapi.ResourceAllocationResponse {
	allocInfo := resp.AllocationResult.ResourceAllocation[string(v1.ResourceMemory)]
	if allocInfo != nil {
		if allocInfo.Annotations == nil {
			allocInfo.Annotations = make(map[string]string)
		}
		allocInfo.Annotations["rdt.resources.beta.kubernetes.io/pod"] = "shared-30"
	}
	return resp
}

func NewPodAdmitter(nodePreempter *NodePreempter, podSubgrouper *PodGrouper) *PodAdmitter {
	return &PodAdmitter{
		nodePreempter: nodePreempter,
		podSubgrouper: podSubgrouper,
	}
}
