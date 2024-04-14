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

package lifecycle

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/pointer"

	configapis "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/options"
)

func TestCNCLifecycle_Run(t *testing.T) {
	t.Parallel()

	type fields struct {
		node *corev1.Node
		cnc  *configapis.CustomNodeConfig
	}
	tests := []struct {
		name    string
		fields  fields
		wantCNC *configapis.CustomNodeConfig
	}{
		{
			name: "test-create",
			fields: fields{
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"test": "test",
						},
					},
				},
			},
			wantCNC: &configapis.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test": "test",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "v1",
							Kind:               "Node",
							Name:               "node1",
							UID:                "",
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
			},
		},
		{
			name: "test-update",
			fields: fields{
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"test": "test-1",
						},
					},
				},
				cnc: &configapis.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"test": "test",
						},
					},
				},
			},
			wantCNC: &configapis.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test": "test-1",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "v1",
							Kind:               "Node",
							Name:               "node1",
							UID:                "",
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{tt.fields.node}, []runtime.Object{tt.fields.cnc})
			assert.NoError(t, err)

			conf, err := options.NewOptions().Config()
			require.NoError(t, err)
			require.NotNil(t, conf)

			cl, err := NewCNCLifecycle(context.Background(),
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.ControllersConfiguration.CNCLifecycleConfig,
				genericCtx.Client,
				genericCtx.KubeInformerFactory.Core().V1().Nodes(),
				genericCtx.InternalInformerFactory.Config().V1alpha1().CustomNodeConfigs(),
				genericCtx.EmitterPool.GetDefaultMetricsEmitter(),
			)
			assert.NoError(t, err)

			// test cache not synced
			err = cl.sync(tt.fields.node.Name)
			assert.NoError(t, err)

			genericCtx.KubeInformerFactory.Start(cl.ctx.Done())
			genericCtx.InternalInformerFactory.Start(cl.ctx.Done())
			go cl.Run()

			cache.WaitForCacheSync(cl.ctx.Done(), cl.nodeListerSynced, cl.cncListerSynced)
			time.Sleep(1 * time.Second)

			gotCNC, err := cl.cncLister.Get(tt.fields.node.Name)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantCNC, gotCNC)

			// test recreate
			err = cl.client.InternalClient.ConfigV1alpha1().CustomNodeConfigs().Delete(context.Background(), tt.fields.node.Name, metav1.DeleteOptions{})
			assert.NoError(t, err)
			time.Sleep(3 * time.Second)

			gotCNC, err = cl.cncLister.Get(tt.fields.node.Name)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantCNC, gotCNC)
		})
	}
}

func TestCNCLifecycle_updateOrCreateCNC(t *testing.T) {
	t.Parallel()

	type fields struct {
		node *corev1.Node
		cnc  *configapis.CustomNodeConfig
	}
	tests := []struct {
		name    string
		fields  fields
		wantCNC *configapis.CustomNodeConfig
	}{
		{
			name: "test-update",
			fields: fields{
				node: &corev1.Node{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"test": "test-1",
						},
					},
				},
				cnc: &configapis.CustomNodeConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name: "node1",
						Labels: map[string]string{
							"test": "test",
						},
					},
				},
			},
			wantCNC: &configapis.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node1",
					Labels: map[string]string{
						"test": "test-1",
					},
					OwnerReferences: []metav1.OwnerReference{
						{
							APIVersion:         "v1",
							Kind:               "Node",
							Name:               "node1",
							UID:                "",
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			genericCtx, err := katalyst_base.GenerateFakeGenericContext([]runtime.Object{tt.fields.node}, []runtime.Object{tt.fields.cnc})
			assert.NoError(t, err)

			conf, err := options.NewOptions().Config()
			require.NoError(t, err)
			require.NotNil(t, conf)

			cl, err := NewCNCLifecycle(context.Background(),
				conf.GenericConfiguration,
				conf.GenericControllerConfiguration,
				conf.ControllersConfiguration.CNCLifecycleConfig,
				genericCtx.Client,
				genericCtx.KubeInformerFactory.Core().V1().Nodes(),
				genericCtx.InternalInformerFactory.Config().V1alpha1().CustomNodeConfigs(),
				genericCtx.EmitterPool.GetDefaultMetricsEmitter(),
			)
			assert.NoError(t, err)

			// test cache not synced
			err = cl.updateOrCreateCNC(tt.fields.node)
			assert.NoError(t, err)
		})
	}
}
