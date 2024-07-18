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

package nodeovercommitment

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"

	"github.com/kubewharf/katalyst-core/pkg/scheduler/plugins/nodeovercommitment/cache"
)

var _ framework.FilterPlugin = &NodeOvercommitment{}

const (
	// Name is the name of the plugin used in the plugin registry and configurations.
	Name = "NodeOvercommitment"

	preFilterStateKey = "PreFilter" + Name
)

var (
	_ framework.PreFilterPlugin = &NodeOvercommitment{}
	_ framework.FilterPlugin    = &NodeOvercommitment{}
	_ framework.ReservePlugin   = &NodeOvercommitment{}
)

type NodeOvercommitment struct{}

func (n *NodeOvercommitment) Name() string {
	return Name
}

func New(args runtime.Object, h framework.Handle) (framework.Plugin, error) {
	klog.Info("Creating new NodeOvercommitment plugin")

	cache.RegisterPodHandler()
	cache.RegisterCNRHandler()
	cache.RegisterNOCHandler()
	return &NodeOvercommitment{}, nil
}
