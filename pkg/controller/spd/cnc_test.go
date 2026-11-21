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

package spd

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/pointer"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestSPDController_waitForCNCCache(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cnc := &configapi.CustomNodeConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "node1"},
		Status: configapi.CustomNodeConfigStatus{
			ServiceProfileConfigList: []configapi.TargetConfig{{ConfigName: "sts1", ConfigNamespace: "default"}},
		},
	}
	controlCtx, err := katalystbase.GenerateFakeGenericContext(nil, []runtime.Object{cnc})
	require.NoError(t, err)
	listStarted, releaseList := make(chan struct{}), make(chan struct{})
	controlCtx.Client.InternalClient.(*internalfake.Clientset).PrependReactor("list", "customnodeconfigs",
		func(clienttesting.Action) (bool, runtime.Object, error) {
			close(listStarted)
			select {
			case <-releaseList:
				return true, &configapi.CustomNodeConfigList{Items: []configapi.CustomNodeConfig{*cnc.DeepCopy()}}, nil
			case <-ctx.Done():
				return true, nil, ctx.Err()
			}
		})
	spdController, err := NewSPDController(ctx, controlCtx, &generic.GenericConfiguration{},
		&controller.GenericControllerConfiguration{}, &controller.SPDConfig{}, generic.NewQoSConfiguration(), struct{}{})
	require.NoError(t, err)
	defer spdController.cncCacheController.cncSyncQueue.ShutDown()
	cncInformer := controlCtx.InternalInformerFactory.Config().V1alpha1().CustomNodeConfigs().Informer()
	podInformer := controlCtx.KubeInformerFactory.Core().V1().Pods().Informer()
	spdInformer := controlCtx.InternalInformerFactory.Workload().V1alpha1().ServiceProfileDescriptors().Informer()
	go podInformer.Run(ctx.Done())
	go spdInformer.Run(ctx.Done())
	otherCaches := []cache.InformerSynced{podInformer.HasSynced, spdInformer.HasSynced}
	for _, informer := range controlCtx.DynamicResourcesManager.GetDynamicInformers() {
		go informer.Informer.Informer().Run(ctx.Done())
		otherCaches = append(otherCaches, informer.Informer.Informer().HasSynced)
	}
	require.True(t, cache.WaitForCacheSync(ctx.Done(), otherCaches...))
	// The fake client serializes reactors, so start the delayed list only after the other caches are ready.
	go cncInformer.Run(ctx.Done())
	select {
	case <-listStarted:
	case <-ctx.Done():
		t.Fatal("CNC list did not start")
	}
	require.False(t, cncInformer.HasSynced())
	ready := true
	for _, synced := range spdController.syncedFunc {
		ready = ready && synced()
	}
	assert.False(t, ready, "CNC cleanup must not start before its initial list completes")
	close(releaseList)
	require.True(t, cache.WaitForCacheSync(ctx.Done(), spdController.syncedFunc...))
	spdController.cncCacheController.clearUnusedConfig()
	updated, err := controlCtx.Client.InternalClient.ConfigV1alpha1().CustomNodeConfigs().Get(ctx, cnc.Name, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Empty(t, updated.Status.ServiceProfileConfigList)
}

func Test_cncCacheController_Run(t *testing.T) {
	t.Parallel()

	type fields struct {
		pod                    *v1.Pod
		workload               *appsv1.StatefulSet
		spd                    *apiworkload.ServiceProfileDescriptor
		cnc                    *configapi.CustomNodeConfig
		spdPodLabelIndexerKeys []string
		enableCNCCache         bool
	}
	tests := []struct {
		name    string
		fields  fields
		wantCNC *configapi.CustomNodeConfig
	}{
		{
			name: "update cnc spd config when cnc cache enable and spd pod label indexer keys not empty",
			fields: fields{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       "sts1",
							},
						},
						Annotations: map[string]string{
							consts.PodAnnotationSPDNameKey: "spd1",
						},
						Labels: map[string]string{
							"workload": "sts1",
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
					},
				},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sts1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.WorkloadAnnotationSPDEnableKey: consts.WorkloadAnnotationSPDEnabled,
						},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
						Template: v1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									"katalyst.kubewharf.io/qos_level": "dedicated_cores",
								},
							},
							Spec: v1.PodSpec{},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "sts1",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       "sts1",
							},
						},
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(100),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{
						AggMetrics: []apiworkload.AggPodMetrics{},
					},
				},
				cnc: &configapi.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
					},
				},
				enableCNCCache:         true,
				spdPodLabelIndexerKeys: []string{"workload"},
			},
			wantCNC: &configapi.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
				},
				Status: configapi.CustomNodeConfigStatus{
					ServiceProfileConfigList: []configapi.TargetConfig{
						{
							ConfigNamespace: "default",
							ConfigName:      "sts1",
							Hash:            "51131be1b092",
						},
					},
				},
			},
		},
		{
			name: "update cnc spd config when cnc cache enable",
			fields: fields{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       "sts1",
							},
						},
						Annotations: map[string]string{
							consts.PodAnnotationSPDNameKey: "spd1",
						},
						Labels: map[string]string{
							"workload": "sts1",
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
					},
				},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sts1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.WorkloadAnnotationSPDEnableKey: consts.WorkloadAnnotationSPDEnabled,
						},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
						Template: v1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									"katalyst.kubewharf.io/qos_level": "dedicated_cores",
								},
							},
							Spec: v1.PodSpec{},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "sts1",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       "sts1",
							},
						},
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(100),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{
						AggMetrics: []apiworkload.AggPodMetrics{},
					},
				},
				cnc: &configapi.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
					},
				},
				enableCNCCache: true,
			},
			wantCNC: &configapi.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
				},
				Status: configapi.CustomNodeConfigStatus{
					ServiceProfileConfigList: []configapi.TargetConfig{
						{
							ConfigNamespace: "default",
							ConfigName:      "sts1",
							Hash:            "51131be1b092",
						},
					},
				},
			},
		},
		{
			name: "clear cnc spd config when cnc cache disable",
			fields: fields{
				pod: &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       "sts1",
							},
						},
						Annotations: map[string]string{
							consts.PodAnnotationSPDNameKey: "spd1",
						},
						Labels: map[string]string{
							"workload": "sts1",
						},
					},
					Spec: v1.PodSpec{
						NodeName: "node1",
					},
				},
				workload: &appsv1.StatefulSet{
					TypeMeta: metav1.TypeMeta{
						Kind:       "StatefulSet",
						APIVersion: "apps/v1",
					},
					ObjectMeta: metav1.ObjectMeta{
						Name:      "sts1",
						Namespace: "default",
						Annotations: map[string]string{
							consts.WorkloadAnnotationSPDEnableKey: consts.WorkloadAnnotationSPDEnabled,
						},
					},
					Spec: appsv1.StatefulSetSpec{
						Selector: &metav1.LabelSelector{
							MatchLabels: map[string]string{
								"workload": "sts1",
							},
						},
						Template: v1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									"katalyst.kubewharf.io/qos_level": "dedicated_cores",
								},
							},
							Spec: v1.PodSpec{},
						},
					},
				},
				spd: &apiworkload.ServiceProfileDescriptor{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: "default",
						Name:      "sts1",
						OwnerReferences: []metav1.OwnerReference{
							{
								APIVersion: "apps/v1",
								Kind:       "StatefulSet",
								Name:       "sts1",
							},
						},
					},
					Spec: apiworkload.ServiceProfileDescriptorSpec{
						TargetRef: apis.CrossVersionObjectReference{
							Kind:       stsGVK.Kind,
							Name:       "sts1",
							APIVersion: stsGVK.GroupVersion().String(),
						},
						BaselinePercent: pointer.Int32(100),
					},
					Status: apiworkload.ServiceProfileDescriptorStatus{
						AggMetrics: []apiworkload.AggPodMetrics{},
					},
				},
				cnc: &configapi.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
					},
					Status: configapi.CustomNodeConfigStatus{
						ServiceProfileConfigList: []configapi.TargetConfig{
							{
								ConfigNamespace: "default",
								ConfigName:      "sts1",
								Hash:            "51131be1b092",
							},
						},
					},
				},
				enableCNCCache: false,
			},
			wantCNC: &configapi.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
				},
				Status: configapi.CustomNodeConfigStatus{},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spdConfig := &controller.SPDConfig{
				EnableCNCCache:         tt.fields.enableCNCCache,
				SPDPodLabelIndexerKeys: tt.fields.spdPodLabelIndexerKeys,
				SPDWorkloadGVResources: []string{"statefulsets.v1.apps"},
			}
			genericConfig := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: []string{"statefulsets.v1.apps"},
			}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			controlCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{tt.fields.pod},
				[]runtime.Object{tt.fields.spd, tt.fields.cnc}, []runtime.Object{tt.fields.workload})
			assert.NoError(t, err)

			spdController, err := NewSPDController(ctx, controlCtx, genericConfig, controllerConf,
				spdConfig, generic.NewQoSConfiguration(), struct{}{})
			assert.NoError(t, err)

			controlCtx.StartInformer(ctx)
			go spdController.Run()
			synced := cache.WaitForCacheSync(ctx.Done(), spdController.syncedFunc...)
			require.True(t, synced)
			spdController.cncCacheController.clearUnusedConfig()
			require.Eventually(t, func() bool {
				newCNC, err := controlCtx.Client.InternalClient.ConfigV1alpha1().CustomNodeConfigs().
					Get(ctx, tt.fields.cnc.Name, metav1.GetOptions{})
				return err == nil && reflect.DeepEqual(tt.wantCNC, newCNC)
			}, time.Second, 10*time.Millisecond)

			newCNC, err := controlCtx.Client.InternalClient.ConfigV1alpha1().CustomNodeConfigs().
				Get(ctx, tt.fields.cnc.Name, metav1.GetOptions{})
			assert.NoError(t, err)
			assert.Equal(t, tt.wantCNC, newCNC)
		})
	}
}
