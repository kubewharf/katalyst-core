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

package cnr

import (
	"context"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/typed/node/v1alpha1"
)

// CNRFetcher is used to get CNR information.
type CNRFetcher interface {
	// GetCNR returns those latest custom node resources metadata.
	GetCNR(ctx context.Context) (*nodev1alpha1.CustomNodeResource, error)
}

type remoteCNRFetcher struct {
	nodeName string
	client   v1alpha1.CustomNodeResourceInterface
}

func NewRemoteCNRFetcher(nodeName string, client v1alpha1.CustomNodeResourceInterface) CNRFetcher {
	return &remoteCNRFetcher{
		nodeName: nodeName,
		client:   client,
	}
}

func (r *remoteCNRFetcher) GetCNR(ctx context.Context) (*nodev1alpha1.CustomNodeResource, error) {
	klog.Warningf("use remote CNR fetcher notice with API requests")
	return r.client.Get(ctx, r.nodeName, v1.GetOptions{ResourceVersion: "0"})
}
