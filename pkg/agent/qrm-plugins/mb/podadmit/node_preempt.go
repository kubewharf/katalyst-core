package podadmit

import (
	"fmt"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/task"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// NodePreempter preempts specified numa nodes (and all their CCDs) before there is load (e.g. socket pod) occupying them
type NodePreempter struct {
	domainManager *mbdomain.MBDomainManager
	mbController  *controller.Controller
	taskManager   task.Manager
}

func (n *NodePreempter) getNotInUseNodes(nodes []uint64) []int {
	var notInUses []int
	inUses := n.taskManager.GetNumaNodesInUse()
	for _, node := range nodes {
		if inUses.Has(int(node)) {
			continue
		}

		notInUses = append(notInUses, int(node))
	}

	return notInUses
}

func (n *NodePreempter) PreemptNodes(req *pluginapi.ResourceRequest) error {
	general.InfofV(6, "mbm: preempt nodes for pod %s/%s", req.PodNamespace, req.PodName)

	if req.Hint != nil {
		if len(req.Hint.Nodes) == 0 {
			return fmt.Errorf("hint is empty")
		}
	}

	// check numa nodes' in-use state; only preempt those not-in-use yet
	nodesToPreempt := n.getNotInUseNodes(req.Hint.Nodes)
	if len(nodesToPreempt) > 0 {
		if n.domainManager.PreemptNodes(nodesToPreempt) {
			// requests to adjust mb ASAP for new preemption if there are any changes
			n.mbController.ReqToAdjustMB()
		}
	}

	return nil
}

func NewNodePreempter(domainManager *mbdomain.MBDomainManager, mbController *controller.Controller, taskManager task.Manager) *NodePreempter {
	return &NodePreempter{
		domainManager: domainManager,
		mbController:  mbController,
		taskManager:   taskManager,
	}
}
