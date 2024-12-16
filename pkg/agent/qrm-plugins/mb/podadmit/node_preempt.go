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

package podadmit

import (
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// NodePreempter preempts specified numa nodes (and all their CCDs) before there is load (e.g. socket pod) occupying them
type NodePreempter struct {
	domainManager *mbdomain.MBDomainManager
	mbController  *controller.Controller
}

func (n *NodePreempter) splitDedicatedNodesToNotInAndInUses(nodes []uint64) (notInUses, inUses []int) {
	nodesDedicated := n.mbController.GetDedicatedNodes()
	for _, node := range nodes {
		if _, ok := nodesDedicated[int(node)]; ok {
			// already a dedicated numa node; should incubate in that case
			inUses = append(inUses, int(node))
			continue
		}
		notInUses = append(notInUses, int(node))
	}

	return notInUses, inUses
}

func (n *NodePreempter) PreemptNodes(req *pluginapi.ResourceRequest) error {
	general.InfofV(6, "mbm: preempt nodes for pod %s/%s", req.PodNamespace, req.PodName)

	if req.Hint != nil {
		if len(req.Hint.Nodes) == 0 {
			return fmt.Errorf("hint is empty")
		}
	}

	general.InfofV(6, "mbm: preempt nodes for pod %s/%s, hinted nodes %v", req.PodNamespace, req.PodName, req.Hint.Nodes)
	nodesToPreempt, nodesToIncubateJustInCase := n.splitDedicatedNodesToNotInAndInUses(req.Hint.Nodes)
	if len(nodesToPreempt) > 0 {
		general.InfofV(6, "mbm: preempt nodes %v for pod %s/%s", nodesToPreempt, req.PodNamespace, req.PodName)
		if n.domainManager.PreemptNodes(nodesToPreempt) {
			// requests to adjust mb ASAP for new preemption if there are any changes
			n.mbController.ReqToAdjustMB()
		}
	}

	// if a node has traffic for some(e.g. previous state not cleaned up) reason, incubate just in case
	if len(nodesToIncubateJustInCase) > 0 {
		general.InfofV(6, "mbm: just in case - incubate nodes %v for pod %s/%s", nodesToIncubateJustInCase, req.PodNamespace, req.PodName)
		n.domainManager.IncubateNodes(nodesToIncubateJustInCase)
	}

	return nil
}

func NewNodePreempter(domainManager *mbdomain.MBDomainManager, mbController *controller.Controller) *NodePreempter {
	return &NodePreempter{
		domainManager: domainManager,
		mbController:  mbController,
	}
}
