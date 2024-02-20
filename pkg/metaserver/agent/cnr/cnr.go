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

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/typed/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
)

// CNRFetcher is used to get CNR information.
type CNRFetcher interface {
	// GetCNR returns those latest custom node resources metadata.
	GetCNR(ctx context.Context) (*nodev1alpha1.CustomNodeResource, error)

	// RegisterNotifier registers a notifier to be notified when CNR is updated.
	RegisterNotifier(name string, notifier CNRNotifier) error

	// UnregisterNotifier unregisters a notifier.
	UnregisterNotifier(name string) error
}

// CNRNotifier is used to notify CNR update.
type CNRNotifier interface {
	// OnCNRUpdate is called when CNR is updated.
	OnCNRUpdate(cnr *nodev1alpha1.CustomNodeResource)

	// OnCNRStatusUpdate is called when CNR status is updated.
	OnCNRStatusUpdate(cnr *nodev1alpha1.CustomNodeResource)
}

type cachedCNRFetcher struct {
	sync.Mutex

	baseConf *global.BaseConfiguration
	cnrConf  *metaserver.CNRConfiguration
	client   v1alpha1.CustomNodeResourceInterface

	cnr          *nodev1alpha1.CustomNodeResource
	lastSyncTime time.Time
	notifiers    map[string]CNRNotifier
}

func NewCachedCNRFetcher(baseConf *global.BaseConfiguration, cnrConf *metaserver.CNRConfiguration, client v1alpha1.CustomNodeResourceInterface) CNRFetcher {
	return &cachedCNRFetcher{
		baseConf:  baseConf,
		cnrConf:   cnrConf,
		client:    client,
		notifiers: make(map[string]CNRNotifier),
	}
}

// GetCNR returns latest CNR from cache, if the cache is expired, it will sync CNR from remote,
// and if the sync fails, it will not update the cache and return the last success cached CNR.
func (c *cachedCNRFetcher) GetCNR(ctx context.Context) (*nodev1alpha1.CustomNodeResource, error) {
	c.Lock()
	defer c.Unlock()

	now := time.Now()
	if c.lastSyncTime.Add(c.cnrConf.CNRCacheTTL).Before(now) {
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
	cnr, err := c.client.Get(ctx, c.baseConf.NodeName, v1.GetOptions{ResourceVersion: "0"})
	if err != nil {
		klog.Errorf("syncCNR failed: %v", err)
		return
	}

	c.cnr = cnr

	if apiequality.Semantic.DeepEqual(cnr.Spec, c.cnr.Spec) ||
		apiequality.Semantic.DeepEqual(cnr.ObjectMeta, c.cnr.ObjectMeta) {
		// notify all notifiers
		for _, notifier := range c.notifiers {
			notifier.OnCNRUpdate(cnr)
		}
	}

	if apiequality.Semantic.DeepEqual(cnr.Status, c.cnr.Status) {
		// notify all notifiers
		for _, notifier := range c.notifiers {
			notifier.OnCNRStatusUpdate(cnr)
		}
	}
}

// RegisterNotifier registers a notifier to the fetcher, it returns error if the notifier is already registered,
// so that the notifier can be registered only once or unregistered it before registering again.
func (c *cachedCNRFetcher) RegisterNotifier(name string, notifier CNRNotifier) error {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.notifiers[name]; ok {
		return fmt.Errorf("notifier %s already registered", name)
	}

	c.notifiers[name] = notifier
	return nil
}

// UnregisterNotifier unregisters a notifier from the fetcher.
func (c *cachedCNRFetcher) UnregisterNotifier(name string) error {
	c.Lock()
	defer c.Unlock()

	if _, ok := c.notifiers[name]; !ok {
		return fmt.Errorf("notifier %s not found", name)
	}

	delete(c.notifiers, name)
	return nil
}
