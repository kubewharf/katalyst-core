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

package config

import (
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	testTargetGVR = metav1.GroupVersionResource{
		Group:    v1alpha1.SchemeGroupVersion.Group,
		Version:  v1alpha1.SchemeGroupVersion.Version,
		Resource: v1alpha1.ResourceNameEvictionConfigurations,
	}
)

func generateTestGenericClientSet(objects ...runtime.Object) *client.GenericClientSet {
	scheme := runtime.NewScheme()
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	return &client.GenericClientSet{
		KubeClient:     nil,
		InternalClient: internalfake.NewSimpleClientset(objects...),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(scheme, objects...),
	}
}

func constructKatalystCustomConfigLoader() ConfigurationLoader {
	nodeName := "test-node"
	c := &v1alpha1.CustomNodeConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
		Status: v1alpha1.CustomNodeConfigStatus{
			KatalystCustomConfigList: []v1alpha1.TargetConfig{
				{
					ConfigName:      "default",
					ConfigNamespace: "test-namespace",
					ConfigType:      testTargetGVR,
					Hash:            "e39c2dd73aac",
				},
			},
		},
	}

	ec := &v1alpha1.EvictionConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: "test-namespace",
		},
		Spec: v1alpha1.EvictionConfigurationSpec{
			Config: v1alpha1.EvictionConfig{
				EvictionPluginsConfig: v1alpha1.EvictionPluginsConfig{
					ReclaimedResourcesEvictionPluginConfig: v1alpha1.ReclaimedResourcesEvictionPluginConfig{
						EvictionThreshold: map[v1.ResourceName]float64{
							v1.ResourceCPU:    1.2,
							v1.ResourceMemory: 1.3,
						},
					},
				},
			},
		},
	}

	clientSet := generateTestGenericClientSet(c, ec)
	cncFetcher := cnc.NewCachedCNCFetcher(nodeName, 1*time.Second,
		clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())

	return NewKatalystCustomConfigLoader(clientSet, 1*time.Second, cncFetcher)
}

func Test_katalystCustomConfigLoader_LoadConfig(t *testing.T) {
	type args struct {
		ctx  context.Context
		gvr  metav1.GroupVersionResource
		conf interface{}
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name: "test-1",
			args: args{
				ctx:  context.TODO(),
				gvr:  testTargetGVR,
				conf: &v1alpha1.EvictionConfiguration{},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := constructKatalystCustomConfigLoader()
			if err := c.LoadConfig(tt.args.ctx, tt.args.gvr, tt.args.conf); (err != nil) != tt.wantErr {
				t.Errorf("LoadConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
