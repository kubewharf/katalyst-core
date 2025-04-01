package mongroups

import (
	"encoding/json"

	v1 "k8s.io/api/core/v1"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type Manager struct {
	policy *policy
}

type policy struct {
	EnabledClosIDs string `json:"enabled-closids,omitempty"`
}

func NewManager(conf *config.Configuration) *Manager {
	mgr := &Manager{
		policy: &policy{},
	}
	if conf.MonGroupsPolicy != "" {
		if err := json.Unmarshal([]byte(conf.MonGroupsPolicy), mgr.policy); err != nil {
			general.Errorf("unmarshal mon_groups policy %s error: %v", conf.MonGroupsPolicy, err)
		}
	}
	return mgr
}

func (m *Manager) Allocate(req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse, qosLevel string, origReqAnno map[string]string) {
	allocInfo := resp.AllocationResult.ResourceAllocation[string(v1.ResourceMemory)]
	if allocInfo == nil {
		return
	}
	if allocInfo.Annotations == nil {
		allocInfo.Annotations = make(map[string]string)
	}
	if m.policy.EnabledClosIDs != "" {
		allocInfo.Annotations[util.AnnotationMonGroupsEnabledClosIDs] = m.policy.EnabledClosIDs
	}
}
