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

package cnc

import (
	"context"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/typed/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
)

type CNCFetcher interface {
	// GetCNC returns latest cnc metadata no deep-copy.
	GetCNC(ctx context.Context) (*v1alpha1.CustomNodeConfig, error)
}

type cachedCNCFetcher struct {
	sync.Mutex
	cnc          *v1alpha1.CustomNodeConfig
	lastSyncTime time.Time

	baseConf *global.BaseConfiguration
	cncConf  *metaserver.CNCConfiguration
	client   configv1alpha1.CustomNodeConfigInterface
}

func NewCachedCNCFetcher(baseConf *global.BaseConfiguration, cncConf *metaserver.CNCConfiguration, client configv1alpha1.CustomNodeConfigInterface) CNCFetcher {
	return &cachedCNCFetcher{
		baseConf: baseConf,
		cncConf:  cncConf,
		client:   client,
	}
}

func (c *cachedCNCFetcher) GetCNC(ctx context.Context) (*v1alpha1.CustomNodeConfig, error) {
	c.Lock()
	defer c.Unlock()

	now := time.Now()
	if c.lastSyncTime.Add(c.cncConf.CustomNodeConfigCacheTTL).Before(now) {
		c.syncCNC(ctx)
		c.lastSyncTime = now
	}

	if c.cnc != nil {
		return c.cnc, nil
	}

	return nil, fmt.Errorf("cannot get cnc from cache and remote")
}

func (c *cachedCNCFetcher) syncCNC(ctx context.Context) {
	klog.Info("[cnc] sync cnc from remote")
	cnc, err := c.client.Get(ctx, c.baseConf.NodeName, v1.GetOptions{ResourceVersion: "0"})
	if err != nil {
		klog.Errorf("syncCNC failed: %v", err)
		return
	}

	c.cnc = cnc
}
