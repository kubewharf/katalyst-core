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

package strategygroup

import (
	"context"
	"fmt"
	"hash/crc32"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	v1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	katalystconfigclient "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/typed/config/v1alpha1"
	configpkg "github.com/kubewharf/katalyst-core/pkg/config"
)

// StrategyGroupController is responsible for creating StrategyGroup objects based on StrategyGroupConfiguration objects.
type StrategyGroupController struct {
	ctx      context.Context
	client   katalystconfigclient.StrategyGroupInterface
	config   *configpkg.Configuration
	informer cache.SharedIndexInformer
}

// NewStrategyGroupController creates a new StrategyGroupController.
func NewStrategyGroupController(
	ctx context.Context,
	client katalystconfigclient.StrategyGroupInterface,
	config *configpkg.Configuration,
	informer cache.SharedIndexInformer,
) (*StrategyGroupController, error) {
	return &StrategyGroupController{
		ctx:      ctx,
		client:   client,
		config:   config,
		informer: informer,
	}, nil
}

// Run starts the StrategyGroupController.
func (c *StrategyGroupController) Run() {
	stopCh := c.ctx.Done()
	c.informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.handleAddStrategyGroupConfiguration,
		UpdateFunc: c.handleUpdateStrategyGroupConfiguration,
		DeleteFunc: c.handleDeleteStrategyGroupConfiguration,
	})

	go c.informer.Run(stopCh)

	if !cache.WaitForCacheSync(stopCh, c.informer.HasSynced) {
		klog.Errorf("Timed out waiting for caches to sync")
		return
	}

	klog.Infof("StrategyGroupController is running")
}

// Stop stops the StrategyGroupController.
func (c *StrategyGroupController) handleAddStrategyGroupConfiguration(obj interface{}) {
	config, ok := obj.(*v1alpha1.StrategyGroupConfiguration)
	if !ok {
		klog.Errorf("Received unexpected object type: %T", obj)
		return
	}

	klog.Infof("Received new StrategyGroupConfiguration: %s", config.Name)
	c.createStrategyGroups(config)
}

// handleUpdateStrategyGroupConfiguration handles updates to StrategyGroupConfiguration objects.
func (c *StrategyGroupController) handleUpdateStrategyGroupConfiguration(oldObj, newObj interface{}) {
	newConfig, ok := newObj.(*v1alpha1.StrategyGroupConfiguration)
	if !ok {
		klog.Errorf("Received unexpected object type: %T", newObj)
		return
	}

	klog.Infof("Received updated StrategyGroupConfiguration: %s", newConfig.Name)
	c.createStrategyGroups(newConfig)
}

// handleDeleteStrategyGroupConfiguration handles deletions to StrategyGroupConfiguration objects.
func (c *StrategyGroupController) handleDeleteStrategyGroupConfiguration(obj interface{}) {
	config, ok := obj.(*v1alpha1.StrategyGroupConfiguration)
	if !ok {
		klog.Errorf("Received unexpected object type: %T", obj)
		return
	}

	klog.Infof("Received deleted StrategyGroupConfiguration: %s", config.Name)
}

// createStrategyGroups creates StrategyGroup objects based on the provided StrategyGroupConfiguration.
func (c *StrategyGroupController) createStrategyGroups(config *v1alpha1.StrategyGroupConfiguration) {
	for _, config := range config.Spec.Config.GroupConfigs {

		names := make([]string, 0, len(config.EnabledStrategies))
		for _, strategy := range config.EnabledStrategies {
			names = append(names, *strategy.Name)
		}

		strategyGroup := &v1alpha1.StrategyGroup{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("sg-%d", crc32.ChecksumIEEE([]byte(strings.Join(names, ",")))),
			},
		}

		_, err := c.client.Create(c.ctx, strategyGroup, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Failed to create StrategyGroup %s: %v", strategyGroup.Name, err)
		} else {
			klog.Infof("Successfully created StrategyGroup %s", strategyGroup.Name)
		}
	}
}
