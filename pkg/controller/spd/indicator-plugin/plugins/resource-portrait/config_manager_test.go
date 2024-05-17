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

package resource_portrait

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
)

func Test_resourcePortraitConfigManagerImpl_refresh(t *testing.T) {
	t.Parallel()

	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "resource-portrait-auto-created-config",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			ResourcePortraitPluginName: `{"configs": [{"filter":{"namespaces":["kube-system"]},"config":{"metrics":["cpu"]}}]}`,
		},
	}

	cmWithErr := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "resource-portrait-auto-created-config",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			ResourcePortraitPluginName: `{"filter":{"namespaces":["kube-system"]},"config":{"metrics":["cpu"]}}]`,
		},
	}

	rpcIndicators := apiconfig.GlobalResourcePortraitConfiguration{
		Configs: []apiconfig.ResourcePortraitConfiguration{
			{
				Config: apiconfig.ResourcePortraitConfig{
					Metrics: []string{"cpu"},
				},
				Filter: apiconfig.ResourcePortraitFilter{
					Namespaces: []string{"kube-system"},
				},
			},
		},
	}

	type want struct {
		refresh       bool
		globalConfigs *apiconfig.GlobalResourcePortraitConfiguration
	}

	type args struct {
		cm         *v1.ConfigMap
		deployment *appsv1.Deployment

		ns   string
		name string
	}

	tests := []struct {
		name string
		args args
		want want
	}{
		{
			name: "normal",
			args: args{
				cm:         cm,
				deployment: nil,
				ns:         "kube-system",
				name:       "resource-portrait-auto-created-config",
			},
			want: want{
				refresh:       true,
				globalConfigs: &rpcIndicators,
			},
		},
		{
			name: "cm not found",
			args: args{
				cm:         cm,
				deployment: nil,
				ns:         "default",
				name:       "resource-portrait-auto-created-config",
			},
			want: want{
				refresh:       false,
				globalConfigs: nil,
			},
		},
		{
			name: "unmarshal err",
			args: args{
				cm:         cmWithErr,
				deployment: nil,
				ns:         "kube-system",
				name:       "resource-portrait-auto-created-config",
			},
			want: want{
				refresh:       false,
				globalConfigs: nil,
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controlCtx, _ := katalystbase.GenerateFakeGenericContext([]runtime.Object{tt.args.cm})
			m := newResourcePortraitConfigManager(types.NamespacedName{Namespace: tt.args.ns, Name: tt.args.name}, controlCtx.Client.KubeClient.CoreV1(), 30*time.Second)
			m.(*resourcePortraitConfigManagerImpl).refresh()
			if !reflect.DeepEqual(m.(*resourcePortraitConfigManagerImpl).globalConfigs, tt.want.globalConfigs) {
				t.Errorf("resourcePortraitConfigManagerImpl_Refresh() got %v, want %v", m.(*resourcePortraitConfigManagerImpl).globalConfigs, tt.want)
			}
		})
	}
}

func Test_resourcePortraitConfigManagerImpl_Filter(t *testing.T) {
	t.Parallel()

	cm := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "resource-portrait-auto-created-config",
			Namespace: "kube-system",
		},
		Data: map[string]string{
			ResourcePortraitPluginName: `{"configs": [{"filter":{"namespaces":["kube-system"]},"config":{"metrics":["cpu"]}}]}`,
		},
	}

	dp1 := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system"},
	}
	data1, _ := json.Marshal(&dp1)
	utd1 := &unstructured.Unstructured{}
	_ = json.Unmarshal(data1, &utd1.Object)

	dp2 := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
	}
	data2, _ := json.Marshal(&dp2)
	utd2 := &unstructured.Unstructured{}
	_ = json.Unmarshal(data2, &utd2.Object)

	type args struct {
		cm         *v1.ConfigMap
		deployment runtime.Object

		ns   string
		name string
	}

	tests := []struct {
		name string
		args args
		want []apiconfig.ResourcePortraitConfig
	}{
		{
			name: "normal",
			args: args{
				cm:         cm,
				deployment: utd1,
				ns:         "kube-system",
				name:       "resource-portrait-auto-created-config",
			},
			want: []apiconfig.ResourcePortraitConfig{
				{Metrics: []string{"cpu"}},
			},
		},
		{
			name: "empty",
			args: args{
				cm:         cm,
				deployment: utd2,
				ns:         "kube-system",
				name:       "resource-portrait-auto-created-config",
			},
			want: []apiconfig.ResourcePortraitConfig(nil),
		},
		{
			name: "filter when refresh failed",
			args: args{
				cm:         cm,
				deployment: utd1,
				ns:         "default",
				name:       "resource-portrait-auto-created-config",
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			controlCtx, _ := katalystbase.GenerateFakeGenericContext([]runtime.Object{tt.args.cm})
			m := newResourcePortraitConfigManager(types.NamespacedName{Namespace: tt.args.ns, Name: tt.args.name}, controlCtx.Client.KubeClient.CoreV1(), 30*time.Second)
			m.(*resourcePortraitConfigManagerImpl).refresh()
			if cfgs := m.Filter(tt.args.deployment); !reflect.DeepEqual(cfgs, tt.want) {
				t.Errorf("resourcePortraitConfigManagerImpl_Filter() got %v, want %v", cfgs, tt.want)
			}
		})
	}
}
