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

package node

import (
	"context"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
)

// NodeFetcher is used to get K8S Node information.
type NodeFetcher interface {
	// Run starts the preparing logic to collect node metadata.
	Run(ctx context.Context)

	// GetNode returns those latest node metadata.
	GetNode(ctx context.Context) (*v1.Node, error)
}

func NewRemoteNodeFetcher(baseConf *global.BaseConfiguration, nodeConf *metaserver.NodeConfiguration, client corev1.NodeInterface) NodeFetcher {
	return &remoteNodeFetcherImpl{
		baseConf: baseConf,
		nodeConf: nodeConf,
		client:   client,
	}
}

type remoteNodeFetcherImpl struct {
	baseConf *global.BaseConfiguration
	nodeConf *metaserver.NodeConfiguration
	client   corev1.NodeInterface
}

func (r *remoteNodeFetcherImpl) Run(_ context.Context) {}

func (r *remoteNodeFetcherImpl) GetNode(ctx context.Context) (*v1.Node, error) {
	return r.client.Get(ctx, r.baseConf.NodeName, metav1.GetOptions{ResourceVersion: "0"})
}
