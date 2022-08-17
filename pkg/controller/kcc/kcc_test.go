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

package kcc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	kcctarget "github.com/kubewharf/katalyst-core/pkg/controller/kcc/target"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func generateTestDeletionTimestamp() *v1.Time {
	now := v1.Now()
	return &now
}

func TestKatalystCustomConfigController_Run(t *testing.T) {
	type args struct {
		kccList       []runtime.Object
		kccTargetList []runtime.Object
		kccConfig     *config.Configuration
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "kcc target not found",
			args: args{
				kccList: []runtime.Object{
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:      "test-kcc",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: v1.GroupVersionResource{
								Group:    v1alpha1.SchemeGroupVersion.Group,
								Version:  v1alpha1.SchemeGroupVersion.Version,
								Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
							},
							NodeLabelSelectorKey: "aa",
						},
					},
				},
			},
		},
		{
			name: "kcc and kcc target are all valid",
			args: args{
				kccList: []runtime.Object{
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:      "test-kcc",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: v1.GroupVersionResource{
								Group:    v1alpha1.SchemeGroupVersion.Group,
								Version:  v1alpha1.SchemeGroupVersion.Version,
								Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
							},
							NodeLabelSelectorKey: "aa",
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.KatalystAgentConfig{
						TypeMeta: v1.TypeMeta{
							Kind:       "KatalystAgentConfig",
							APIVersion: "config.katalyst.kubewharf.io/v1alpha1",
						},
						ObjectMeta: v1.ObjectMeta{
							Name:      "default",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystAgentConfigSpec{
							Config: v1alpha1.AgentConfig{
								ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
									EvictionThreshold: map[corev1.ResourceName]float64{
										corev1.ResourceCPU: 5.0,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "more than one kcc with same gvr",
			args: args{
				kccList: []runtime.Object{
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:      "test-kcc",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: v1.GroupVersionResource{
								Group:    v1alpha1.SchemeGroupVersion.Group,
								Version:  v1alpha1.SchemeGroupVersion.Version,
								Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
							},
							NodeLabelSelectorKey: "aa",
						},
					},
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:      "test-kcc-1",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: v1.GroupVersionResource{
								Group:    v1alpha1.SchemeGroupVersion.Group,
								Version:  v1alpha1.SchemeGroupVersion.Version,
								Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
							},
							NodeLabelSelectorKey: "bb",
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.KatalystAgentConfig{
						TypeMeta: v1.TypeMeta{
							Kind:       "KatalystAgentConfig",
							APIVersion: "config.katalyst.kubewharf.io/v1alpha1",
						},
						ObjectMeta: v1.ObjectMeta{
							Name:      "default",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystAgentConfigSpec{
							Config: v1alpha1.AgentConfig{
								ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
									EvictionThreshold: map[corev1.ResourceName]float64{
										corev1.ResourceCPU: 5.0,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "handle finalizer terminating",
			args: args{
				kccList: []runtime.Object{
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:              "test-kcc",
							Namespace:         "default",
							DeletionTimestamp: generateTestDeletionTimestamp(),
							Finalizers: []string{
								consts.KatalystCustomConfigFinalizerKCC,
							},
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: v1.GroupVersionResource{
								Group:    v1alpha1.SchemeGroupVersion.Group,
								Version:  v1alpha1.SchemeGroupVersion.Version,
								Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
							},
							NodeLabelSelectorKey: "aa",
						},
					},
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:      "test-kcc-1",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: v1.GroupVersionResource{
								Group:    v1alpha1.SchemeGroupVersion.Group,
								Version:  v1alpha1.SchemeGroupVersion.Version,
								Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
							},
							NodeLabelSelectorKey: "bb",
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.KatalystAgentConfig{
						TypeMeta: v1.TypeMeta{
							Kind:       "KatalystAgentConfig",
							APIVersion: "config.katalyst.kubewharf.io/v1alpha1",
						},
						ObjectMeta: v1.ObjectMeta{
							Name:      "default",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystAgentConfigSpec{
							Config: v1alpha1.AgentConfig{
								ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
									EvictionThreshold: map[corev1.ResourceName]float64{
										corev1.ResourceCPU: 5.0,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "handle finalizer normal",
			args: args{
				kccList: []runtime.Object{
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:              "test-kcc",
							Namespace:         "default",
							DeletionTimestamp: generateTestDeletionTimestamp(),
							Finalizers: []string{
								consts.KatalystCustomConfigFinalizerKCC,
							},
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: v1.GroupVersionResource{
								Group:    v1alpha1.SchemeGroupVersion.Group,
								Version:  v1alpha1.SchemeGroupVersion.Version,
								Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
							},
							NodeLabelSelectorKey: "aa",
						},
					},
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:      "test-kcc-1",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: v1.GroupVersionResource{
								Group:    v1alpha1.SchemeGroupVersion.Group,
								Version:  v1alpha1.SchemeGroupVersion.Version,
								Resource: v1alpha1.ResourceNameKatalystAgentConfigs,
							},
							NodeLabelSelectorKey: "bb",
						},
					},
				},
				kccTargetList: []runtime.Object{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			genericContext, err := katalyst_base.GenerateFakeGenericContext(nil, tt.args.kccList, tt.args.kccTargetList)
			assert.NoError(t, err)
			conf := generateTestConfiguration(t)

			ctx := context.Background()
			targetHandler := kcctarget.NewKatalystCustomConfigTargetHandler(
				ctx,
				genericContext.Client,
				conf.ControllersConfiguration.KCCConfig,
				genericContext.InternalInformerFactory.Config().V1alpha1().KatalystCustomConfigs(),
			)

			kcc, err := NewKatalystCustomConfigController(
				ctx,
				genericContext.Client,
				genericContext.InternalInformerFactory.Config().V1alpha1().KatalystCustomConfigs(),
				conf.GenericControllerConfiguration,
				metrics.DummyMetrics{},
				conf.KCCConfig,
				targetHandler,
			)
			assert.NoError(t, err)

			genericContext.StartInformer(ctx)
			go targetHandler.Run()
			go kcc.Run()

			cache.WaitForCacheSync(kcc.ctx.Done(), kcc.syncedFunc...)
			time.Sleep(1 * time.Second)
		})
	}
}
