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
	"fmt"
	"sync"
	"time"

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

type cachedCNRFetcher struct {
	sync.Mutex

	nodeName string
	client   v1alpha1.CustomNodeResourceInterface

	cnr          *nodev1alpha1.CustomNodeResource
	lastSyncTime time.Time
	ttl          time.Duration
}

func NewCachedCNRFetcher(nodeName string, ttl time.Duration, client v1alpha1.CustomNodeResourceInterface) CNRFetcher {
	return &cachedCNRFetcher{
		nodeName: nodeName,
		ttl:      ttl,
		client:   client,
	}
}

func (c *cachedCNRFetcher) GetCNR(ctx context.Context) (*nodev1alpha1.CustomNodeResource, error) {
	c.Lock()
	defer c.Unlock()

	now := time.Now()
	if c.lastSyncTime.Add(c.ttl).Before(now) {
		c.syncCNR(ctx)
		c.lastSyncTime = now
	}

	if c.cnr != nil {
		return c.cnr, nil
	}

	return nil, fmt.Errorf("cannot get cnr from cache and remote")
}

func (c *cachedCNRFetcher) syncCNR(ctx context.Context) {
	klog.Infof("[cnr] sync cnr from remote")
	cnr, err := c.client.Get(ctx, c.nodeName, v1.GetOptions{ResourceVersion: "0"})
	if err != nil {
		klog.Errorf("syncCNR failed: %v", err)
		return
	}

	c.cnr = cnr
}
