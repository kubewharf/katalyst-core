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
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
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
	t.Parallel()

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
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
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
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.AdminQoSConfiguration{
						TypeMeta: v1.TypeMeta{
							Kind:       "EvictionConfiguration",
							APIVersion: "config.katalyst.kubewharf.io/v1alpha1",
						},
						ObjectMeta: v1.ObjectMeta{
							Name:      "default",
							Namespace: "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
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
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:      "test-kcc-1",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.AdminQoSConfiguration{
						TypeMeta: v1.TypeMeta{
							Kind:       "EvictionConfiguration",
							APIVersion: "config.katalyst.kubewharf.io/v1alpha1",
						},
						ObjectMeta: v1.ObjectMeta{
							Name:      "default",
							Namespace: "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
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
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:      "test-kcc-1",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
				},
				kccTargetList: []runtime.Object{
					&v1alpha1.AdminQoSConfiguration{
						TypeMeta: v1.TypeMeta{
							Kind:       "EvictionConfiguration",
							APIVersion: "config.katalyst.kubewharf.io/v1alpha1",
						},
						ObjectMeta: v1.ObjectMeta{
							Name:      "default",
							Namespace: "default",
						},
						Spec: v1alpha1.AdminQoSConfigurationSpec{
							Config: v1alpha1.AdminQoSConfig{
								EvictionConfig: &v1alpha1.EvictionConfig{
									ReclaimedResourcesEvictionConfig: &v1alpha1.ReclaimedResourcesEvictionConfig{
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
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
						},
					},
					&v1alpha1.KatalystCustomConfig{
						ObjectMeta: v1.ObjectMeta{
							Name:      "test-kcc-1",
							Namespace: "default",
						},
						Spec: v1alpha1.KatalystCustomConfigSpec{
							TargetType: crd.AdminQoSConfigurationGVR,
							NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
								{
									Priority: 0,
									KeyList:  []string{"aa"},
								},
							},
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
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.KCCConfig,
				genericContext.Client,
				genericContext.InternalInformerFactory.Config().V1alpha1().KatalystCustomConfigs(),
				metrics.DummyMetrics{},
				targetHandler,
			)
			assert.NoError(t, err)

			genericContext.StartInformer(ctx)
			go targetHandler.Run()
			go kcc.Run()

			cache.WaitForCacheSync(kcc.ctx.Done(), kcc.syncedFunc...)
			time.Sleep(100 * time.Millisecond)
		})
	}
}

func Test_checkNodeLabelSelectorAllowedKeyList(t *testing.T) {
	t.Parallel()

	type args struct {
		kcc *v1alpha1.KatalystCustomConfig
	}
	tests := []struct {
		name  string
		args  args
		want  string
		want1 bool
	}{
		{
			name: "test-1",
			args: args{
				kcc: &v1alpha1.KatalystCustomConfig{
					Spec: v1alpha1.KatalystCustomConfigSpec{
						NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
							{
								Priority: 0,
								KeyList:  []string{"aa"},
							},
							{
								Priority: 1,
								KeyList:  []string{"cc"},
							},
						},
					},
				},
			},
			want:  "",
			want1: true,
		},
		{
			name: "test-2",
			args: args{
				kcc: &v1alpha1.KatalystCustomConfig{
					Spec: v1alpha1.KatalystCustomConfigSpec{
						NodeLabelSelectorAllowedKeyList: []v1alpha1.PriorityNodeLabelSelectorAllowedKeyList{
							{
								Priority: 0,
								KeyList:  []string{"aa"},
							},
							{
								Priority: 0,
								KeyList:  []string{"cc"},
							},
						},
					},
				},
			},
			want:  "duplicated priority: [0]",
			want1: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, got1 := checkNodeLabelSelectorAllowedKeyList(tt.args.kcc)
			assert.Equalf(t, tt.want, got, "checkNodeLabelSelectorAllowedKeyList(%v)", tt.args.kcc)
			assert.Equalf(t, tt.want1, got1, "checkNodeLabelSelectorAllowedKeyList(%v)", tt.args.kcc)
		})
	}
}
