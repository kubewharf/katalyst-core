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
package config // import "github.com/kubewharf/katalyst-core/pkg/metaserver/config"

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
	// targetConfigMeta records the meta-info for matched configurations in CNC.
	targetConfigMeta v1alpha1.TargetConfig
	// targetConfigContent records the contents for matched configurations in CNC.
	targetConfigContent util.KCCTargetResource
}

type katalystCustomConfigLoader struct {
	client *client.GenericClientSet

	nodeName string
	ttl      time.Duration

	mux sync.RWMutex

	// lastFetchCNCTargetTime is to limit the rate of getting cnc, lastFetchConfigTime is to
	// limit the rate of getting each configuration, this is to avoid getting some configurations
	// frequently when it always fails
	lastFetchCNCTargetTime time.Time
	lastFetchConfigTime    map[metav1.GroupVersionResource]time.Time

	// cncTargetCache is a cache of gvr to cnc target config meta-info including target config
	// namespace/name and hash, configCache is a cache of gvr to current target config meta-info
	// and its latest object
	cncTargetCache map[metav1.GroupVersionResource]v1alpha1.TargetConfig
	configCache    map[metav1.GroupVersionResource]configCache
}

// NewKatalystCustomConfigLoader create a new configManager to fetch KatalystCustomConfig.
// every LoadConfig() call tries to fetch the value from local cache; if it is
// not there, invalidated or too old, we fetch it from api-server and refresh the
// value in cache; otherwise it is just fetched from cache.
// defaultGVRList s the list of default gvr fetched from remote api-server, if
// LoadConfig() fetches a new gvr, it will be automatically added to.
func NewKatalystCustomConfigLoader(clientSet *client.GenericClientSet, ttl time.Duration, nodeName string) ConfigurationLoader {
	return &katalystCustomConfigLoader{
		client:              clientSet,
		nodeName:            nodeName,
		ttl:                 ttl,
		lastFetchConfigTime: make(map[metav1.GroupVersionResource]time.Time),
		configCache:         make(map[metav1.GroupVersionResource]configCache),
		cncTargetCache:      make(map[metav1.GroupVersionResource]v1alpha1.TargetConfig),
	}
}

func (c *katalystCustomConfigLoader) LoadConfig(ctx context.Context, gvr metav1.GroupVersionResource, conf interface{}) error {
	// get current updated cnc to update target cache
	err := c.updateTargetCacheIfNeed(ctx)
	if err != nil {
		klog.Errorf("[kcc-sdk] failed update target cache from remote: %s, use local cache instead", err)
	}

	// get current target config according to its hash to update config cache
	err = c.updateConfigCacheIfNeed(ctx, gvr)
	if err != nil {
		klog.Errorf("[kcc-sdk] failed update config cache from remote: %s, use local cache instead", err)
	}

	c.mux.RLock()
	cache, ok := c.configCache[gvr]
	c.mux.RUnlock()

	if ok {
		return cache.targetConfigContent.Unmarshal(conf)
	}

	return fmt.Errorf("[kcc-sdk] targetConfigMeta for %s not found", gvr)
}

// updateTargetCacheIfNeed updates cnc target cache from remote if the previous is out-of date。
func (c *katalystCustomConfigLoader) updateTargetCacheIfNeed(ctx context.Context) error {
	c.mux.Lock()
	defer c.mux.Unlock()

	if c.lastFetchCNCTargetTime.Add(c.ttl).After(time.Now()) {
		return nil
	}

	// set last update time here to avoid trying to get cnc from
	// remote frequency when target gvr doesn't exist in remote
	c.lastFetchCNCTargetTime = time.Now()

	klog.Infof("[kcc-sdk] getting cnc from remote")
	cnc, err := c.client.InternalClient.ConfigV1alpha1().CustomNodeConfigs().Get(ctx, c.nodeName, metav1.GetOptions{ResourceVersion: "0"})
	if err != nil {
		return err
	}

	targetCache := make(map[metav1.GroupVersionResource]v1alpha1.TargetConfig)
	for _, target := range cnc.Status.KatalystCustomConfigList {
		targetCache[target.ConfigType] = target
	}
	c.cncTargetCache = targetCache

	return nil
}

// updateConfigCacheIfNeed checks if the previous configuration has changed, and
// re-get from APIServer if the previous is out-of date.
func (c *katalystCustomConfigLoader) updateConfigCacheIfNeed(ctx context.Context, gvr metav1.GroupVersionResource) error {
	c.mux.Lock()
	defer c.mux.Unlock()

	target, ok := c.cncTargetCache[gvr]
	if !ok {
		return nil
	}

	if old, ok := c.configCache[gvr]; !ok || target.Hash != old.targetConfigMeta.Hash {
		// update last fetch config timestamp first
		if lastFetchTime, ok := c.lastFetchConfigTime[gvr]; ok && lastFetchTime.Add(c.ttl).After(time.Now()) {
			return nil
		} else {
			c.lastFetchConfigTime[gvr] = time.Now()
		}

		schemaGVR := native.ToSchemaGVR(gvr.Group, gvr.Version, gvr.Resource)
		var dynamicClient dynamic.ResourceInterface
		if target.ConfigNamespace != "" {
			dynamicClient = c.client.DynamicClient.Resource(schemaGVR).Namespace(target.ConfigNamespace)
		} else {
			dynamicClient = c.client.DynamicClient.Resource(schemaGVR)
		}

		// todo: emit metrics if fail to get latest dynamic config from APIServer
		klog.Infof("[kcc-sdk] %s targetConfigMeta hash is changed to %s", gvr, target.Hash)
		conf, err := dynamicClient.Get(ctx, target.ConfigName, metav1.GetOptions{ResourceVersion: "0"})
		if err != nil {
			return err
		}

		c.configCache[gvr] = configCache{
			targetConfigMeta:    target,
			targetConfigContent: util.ToKCCTargetResource(conf),
		}

		klog.Infof("[kcc-sdk] %s targetConfigMeta has been updated to %v", gvr.String(), conf)
	}

	return nil
}
