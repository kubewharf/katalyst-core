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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	v1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	fakeapi "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	configpkg "github.com/kubewharf/katalyst-core/pkg/config"
)

func TestNewStrategyGroupController(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := fakeapi.NewSimpleClientset().ConfigV1alpha1().StrategyGroups()
	config := &configpkg.Configuration{}
	informer := cache.NewSharedIndexInformer(nil, nil, 0, nil)

	controller, err := NewStrategyGroupController(ctx, client, config, informer)
	if err != nil {
		t.Errorf("NewStrategyGroupController returned an error: %v", err)
	}
	if controller == nil {
		t.Errorf("NewStrategyGroupController returned nil controller")
	}
	if controller != nil && controller.ctx != ctx {
		t.Errorf("Expected context %v, but got %v", ctx, controller.ctx)
	}
	if controller != nil && controller.client != client {
		t.Errorf("Expected client %v, but got %v", client, controller.client)
	}
	if controller != nil && controller.config != config {
		t.Errorf("Expected config %v, but got %v", config, controller.config)
	}
	if controller != nil && controller.informer != informer {
		t.Errorf("Expected informer %v, but got %v", informer, controller.informer)
	}
}

func TestStrategyGroupController_Run(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	client := fakeapi.NewSimpleClientset().ConfigV1alpha1().StrategyGroups()
	config := &configpkg.Configuration{}
	informer := cache.NewSharedIndexInformer(nil, nil, 0, nil)
	controller, _ := NewStrategyGroupController(ctx, client, config, informer)

	go controller.Run()
	defer cancel()
}

func TestStrategyGroupController_handleAddStrategyGroupConfiguration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := fakeapi.NewSimpleClientset().ConfigV1alpha1().StrategyGroups()
	config := &configpkg.Configuration{}
	informer := cache.NewSharedIndexInformer(nil, nil, 0, nil)
	controller, _ := NewStrategyGroupController(ctx, client, config, informer)

	sgConfig := &v1alpha1.StrategyGroupConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-config",
		},
	}

	controller.handleAddStrategyGroupConfiguration(sgConfig)
}

func TestStrategyGroupController_handleUpdateStrategyGroupConfiguration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := fakeapi.NewSimpleClientset().ConfigV1alpha1().StrategyGroups()
	config := &configpkg.Configuration{}
	informer := cache.NewSharedIndexInformer(nil, nil, 0, nil)
	controller, _ := NewStrategyGroupController(ctx, client, config, informer)

	oldConfig := &v1alpha1.StrategyGroupConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-config",
		},
	}
	newConfig := &v1alpha1.StrategyGroupConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-config",
		},
	}

	controller.handleUpdateStrategyGroupConfiguration(oldConfig, newConfig)
}

func TestStrategyGroupController_handleDeleteStrategyGroupConfiguration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := fakeapi.NewSimpleClientset().ConfigV1alpha1().StrategyGroups()
	config := &configpkg.Configuration{}
	informer := cache.NewSharedIndexInformer(nil, nil, 0, nil)
	controller, _ := NewStrategyGroupController(ctx, client, config, informer)

	sgConfig := &v1alpha1.StrategyGroupConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-config",
		},
	}

	controller.handleDeleteStrategyGroupConfiguration(sgConfig)
}

func TestStrategyGroupController_createStrategyGroups(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client := fakeapi.NewSimpleClientset().ConfigV1alpha1().StrategyGroups()
	config := &configpkg.Configuration{}
	informer := cache.NewSharedIndexInformer(nil, nil, 0, nil)
	controller, _ := NewStrategyGroupController(ctx, client, config, informer)

	sgConfig := &v1alpha1.StrategyGroupConfiguration{
		Spec: v1alpha1.StrategyGroupConfigurationSpec{
			Config: v1alpha1.StrategyGroupConfig{
				GroupConfigs: []v1alpha1.GroupConfig{
					{
						EnabledStrategies: []v1alpha1.Strategy{
							{
								Name: func() *string { s := "strategy1"; return &s }(),
							},
							{
								Name: func() *string { s := "strategy2"; return &s }(),
							},
						},
					},
				},
			},
		},
	}

	controller.createStrategyGroups(sgConfig)
}
