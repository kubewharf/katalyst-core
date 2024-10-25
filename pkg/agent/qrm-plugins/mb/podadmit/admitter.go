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
	"context"

	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller/mbdomain"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type admitter struct {
	pluginapi.UnimplementedResourcePluginServer
	qosConfig     *generic.QoSConfiguration
	domainManager *mbdomain.MBDomainManager
	mbController  *controller.Controller
}

func (m admitter) GetResourcePluginOptions(context.Context, *pluginapi.Empty) (*pluginapi.ResourcePluginOptions, error) {
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         false,
	}, nil
}

func (m admitter) AllocateForPod(ctx context.Context, request *pluginapi.PodResourceRequest) (*pluginapi.PodResourceAllocationResponse, error) {
	qosLevel, err := m.qosConfig.GetQoSLevel(nil, request.Annotations)
	if err != nil {
		return nil, err
	}

	if qosLevel == apiconsts.PodAnnotationQoSLevelDedicatedCores {
		if request.Hint != nil {
			for _, node := range request.Hint.Nodes {
				m.domainManager.PreemptNodes([]int{int(node)})
			}
			// requests to adjust mb ASAP for new preemption
			m.mbController.ReqToAdjustMB()
		}
	}

	resp := &pluginapi.PodResourceAllocationResponse{
		PodUid:           request.PodUid,
		PodNamespace:     request.PodNamespace,
		PodName:          request.PodName,
		PodRole:          request.PodRole,
		PodType:          request.PodType,
		ResourceName:     "mb-pod-admit",
		AllocationResult: nil,
		Labels:           general.DeepCopyMap(request.Labels),
		Annotations:      general.DeepCopyMap(request.Annotations),
	}

	return resp, nil
}

var _ pluginapi.ResourcePluginServer = (*admitter)(nil)
