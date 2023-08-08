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
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	kcctarget "github.com/kubewharf/katalyst-core/pkg/controller/kcc/target"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestCustomNodeConfigController_Run(t *testing.T) {
	t.Parallel()

	type args struct {
		cncAndKCCList []runtime.Object
		kccTargetList []runtime.Object
		kccConfig     *config.Configuration
	}
	tests := []struct {
		name string
		args args
	}{
		{
			name: "kcc target all valid",
			args: args{
				cncAndKCCList: []runtime.Object{
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
					&v1alpha1.CustomNodeConfig{
						ObjectMeta: v1.ObjectMeta{
							Name: "node-1",
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			genericContext, err := katalyst_base.GenerateFakeGenericContext(nil, tt.args.cncAndKCCList, tt.args.kccTargetList)
			assert.NoError(t, err)
			conf := generateTestConfiguration(t)

			ctx := context.Background()
			targetHandler := kcctarget.NewKatalystCustomConfigTargetHandler(
				ctx,
				genericContext.Client,
				conf.ControllersConfiguration.KCCConfig,
				genericContext.InternalInformerFactory.Config().V1alpha1().KatalystCustomConfigs(),
			)

			cnc, err := NewCustomNodeConfigController(
				ctx,
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.KCCConfig,
				genericContext.Client,
				genericContext.InternalInformerFactory.Config().V1alpha1().CustomNodeConfigs(),
				metrics.DummyMetrics{},
				targetHandler,
			)
			assert.NoError(t, err)

			genericContext.StartInformer(ctx)
			go targetHandler.Run()
			go cnc.Run()

			cache.WaitForCacheSync(cnc.ctx.Done(), cnc.syncedFunc...)
			time.Sleep(1 * time.Second)
		})
	}
}
