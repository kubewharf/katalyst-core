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

package npd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	metaconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

func TestNpdFetcher_GetNPD(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		npd  *nodev1alpha1.NodeProfileDescriptor
	}{
		{
			name: "get npd",
			npd:  fakeNPD(),
		},
	}

	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			nodeName := tt.npd.Name
			c := &v1alpha1.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
				Status: v1alpha1.CustomNodeConfigStatus{
					KatalystCustomConfigList: []v1alpha1.TargetConfig{
						{
							ConfigName: tt.npd.Name,
							ConfigType: util.NPDGVR,
							Hash:       "fake-hash",
						},
					},
				},
			}

			cacheTTL := 1 * time.Second
			scheme := runtime.NewScheme()
			utilruntime.Must(v1alpha1.AddToScheme(scheme))
			utilruntime.Must(nodev1alpha1.AddToScheme(scheme))
			clientSet := &client.GenericClientSet{
				KubeClient:     nil,
				InternalClient: internalfake.NewSimpleClientset(c, tt.npd),
				DynamicClient:  dynamicfake.NewSimpleDynamicClient(scheme, c, tt.npd),
			}
			cncFetcher := cnc.NewCachedCNCFetcher(
				&global.BaseConfiguration{NodeName: nodeName},
				&metaconfig.CNCConfiguration{CustomNodeConfigCacheTTL: cacheTTL},
				clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())

			fetcher := NewNPDFetcher(clientSet, cncFetcher, &metaconfig.KCCConfiguration{ConfigCacheTTL: cacheTTL})
			npd, err := fetcher.GetNPD(context.Background())
			assert.NoError(t, err)
			assert.Equal(t, tt.npd, npd)
		})
	}
}

func fakeNPD() *nodev1alpha1.NodeProfileDescriptor {
	return &nodev1alpha1.NodeProfileDescriptor{
		TypeMeta: metav1.TypeMeta{
			Kind:       "NodeProfileDescriptor",
			APIVersion: nodev1alpha1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-1",
		},
		Status: nodev1alpha1.NodeProfileDescriptorStatus{
			NodeMetrics: []nodev1alpha1.ScopedNodeMetrics{
				{
					Scope: "fake-scope",
					Metrics: []nodev1alpha1.MetricValue{
						{
							MetricName: "cpu",
							Value:      resource.MustParse("4"),
						},
					},
				},
			},
		},
	}
}
