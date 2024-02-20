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

// Package config is the package that gets centralized configurations periodically
// and dynamically for a given node.
package kcc // import "github.com/kubewharf/katalyst-core/pkg/metaserver/config"

import (
	"context"
	"fmt"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// ConfigurationLoader is used to load configurations from centralized server.
type ConfigurationLoader interface {
	LoadConfig(ctx context.Context, gvr metav1.GroupVersionResource, conf interface{}) error
}

// configCache keeps a local in-memory cache for each configuration CRD.
// each time when users want to get the latest configuration, return from
// cache firstly (it still valid); otherwise, trigger a client getting action.
type configCache struct {
	// targetConfigHash records the config hash for matched configurations in CNC.
	targetConfigHash string
	// targetConfigContent records the contents for matched configurations in CNC.
	targetConfigContent util.KCCTargetResource
}

type katalystCustomConfigLoader struct {
	client     *client.GenericClientSet
	cncFetcher cnc.CNCFetcher

	ttl time.Duration

	mux sync.RWMutex

	// lastFetchConfigTime is to limit the rate of getting each configuration,
	// this is to avoid getting some configurations frequently when it always
	// fails
	lastFetchConfigTime map[metav1.GroupVersionResource]time.Time

	// configCache is a cache of gvr to current target config meta-info
	// and its latest object
	configCache map[metav1.GroupVersionResource]configCache
}

// NewKatalystCustomConfigLoader create a new configManager to fetch KatalystCustomConfig.
// every LoadConfig() call tries to fetch the value from local cache; if it is
// not there, invalidated or too old, we fetch it from api-server and refresh the
// value in cache; otherwise it is just fetched from cache.
// defaultGVRList s the list of default gvr fetched from remote api-server, if
// LoadConfig() fetches a new gvr, it will be automatically added to.
func NewKatalystCustomConfigLoader(clientSet *client.GenericClientSet, ttl time.Duration,
	cncFetcher cnc.CNCFetcher) ConfigurationLoader {
	return &katalystCustomConfigLoader{
		cncFetcher:          cncFetcher,
		client:              clientSet,
		ttl:                 ttl,
		lastFetchConfigTime: make(map[metav1.GroupVersionResource]time.Time),
		configCache:         make(map[metav1.GroupVersionResource]configCache),
	}
}

func (c *katalystCustomConfigLoader) LoadConfig(ctx context.Context, gvr metav1.GroupVersionResource, conf interface{}) error {
	// get target config from updated cnc
	targetConfig, err := c.getCNCTargetConfig(ctx, gvr)
	if err != nil {
		return fmt.Errorf("get cnc target cache failed: %v", err)
	}

	// update current target config according to its hash
	err = c.updateConfigCacheIfNeed(ctx, targetConfig)
	if err != nil {
		klog.Errorf("[kcc-sdk] failed update config cache from remote: %s, use local cache instead", err)
	}

	c.mux.RLock()
	cache, ok := c.configCache[gvr]
	c.mux.RUnlock()

	if ok {
		return cache.targetConfigContent.Unmarshal(conf)
	}

	return fmt.Errorf("get config cache for %s not found", gvr)
}

// getCNCTargetConfig get cnc target from cnc fetcher
func (c *katalystCustomConfigLoader) getCNCTargetConfig(ctx context.Context, gvr metav1.GroupVersionResource) (*v1alpha1.TargetConfig, error) {
	currentCNC, err := c.cncFetcher.GetCNC(ctx)
	if err != nil {
		return nil, err
	}

	for _, target := range currentCNC.Status.KatalystCustomConfigList {
		if target.ConfigType == gvr {
			return &target, nil
		}
	}

	return nil, fmt.Errorf("get target config %s not found", gvr)
}

// updateConfigCacheIfNeed checks if the previous configuration has changed, and
// re-get from APIServer if the previous is out-of date.
func (c *katalystCustomConfigLoader) updateConfigCacheIfNeed(ctx context.Context, targetConfig *v1alpha1.TargetConfig) error {
	c.mux.Lock()
	defer c.mux.Unlock()

	if targetConfig == nil {
		return nil
	}

	gvr := targetConfig.ConfigType
	if cache, ok := c.configCache[gvr]; !ok || targetConfig.Hash != cache.targetConfigHash {
		// update last fetch config timestamp first
		if lastFetchTime, ok := c.lastFetchConfigTime[gvr]; ok && lastFetchTime.Add(c.ttl).After(time.Now()) {
			return nil
		} else {
			c.lastFetchConfigTime[gvr] = time.Now()
		}

		schemaGVR := native.ToSchemaGVR(gvr.Group, gvr.Version, gvr.Resource)
		var dynamicClient dynamic.ResourceInterface
		if targetConfig.ConfigNamespace != "" {
			dynamicClient = c.client.DynamicClient.Resource(schemaGVR).Namespace(targetConfig.ConfigNamespace)
		} else {
			dynamicClient = c.client.DynamicClient.Resource(schemaGVR)
		}

		// todo: emit metrics if fail to get latest dynamic config from APIServer
		klog.Infof("[kcc-sdk] %s targetConfigMeta hash is changed to %s", gvr, targetConfig.Hash)
		conf, err := dynamicClient.Get(ctx, targetConfig.ConfigName, metav1.GetOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}

		c.configCache[gvr] = configCache{
			targetConfigHash:    targetConfig.Hash,
			targetConfigContent: util.ToKCCTargetResource(conf),
		}

		klog.Infof("[kcc-sdk] %s config cache has been updated to %v", gvr.String(), conf)
	}

	return nil
}
