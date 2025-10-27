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
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/pkg/client"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	metaconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	rputil "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

const testDir = "/tmp/metaserver_test"

// failingLoader simulates config fetch failures
type failingLoader struct{}

func (f *failingLoader) LoadConfig(ctx context.Context, gvr metav1.GroupVersionResource, conf interface{}) error {
	return fmt.Errorf("simulated load failure")
}

func generateTestConfiguration(t *testing.T, dir string) *pkgconfig.Configuration {
	conf := pkgconfig.NewConfiguration()
	require.NotNil(t, conf)
	conf.CheckpointManagerDir = dir
	conf.ConfigCheckpointGraceTime = 2 * time.Hour
	conf.CNRCacheTTL = 1 * time.Second
	return conf
}

func generateTestNPD(nodeName string) *nodev1alpha1.NodeProfileDescriptor {
	min := nodev1alpha1.AggregatorMin
	return &nodev1alpha1.NodeProfileDescriptor{
		TypeMeta: metav1.TypeMeta{
			Kind:       "NodeProfileDescriptor",
			APIVersion: nodev1alpha1.SchemeGroupVersion.String(),
		},
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Status: nodev1alpha1.NodeProfileDescriptorStatus{
			NodeMetrics: []nodev1alpha1.ScopedNodeMetrics{
				{
					Scope: rputil.MetricScope,
					Metrics: []nodev1alpha1.MetricValue{
						{
							MetricName: "cpu",
							MetricLabels: map[string]string{
								"package-name": "x2",
								"numa-id":      "0",
							},
							Value:      resource.MustParse("32"),
							Aggregator: &min,
						},
						{
							MetricName: "memory",
							MetricLabels: map[string]string{
								"package-name": "x2",
								"numa-id":      "0",
							},
							Value:      resource.MustParse("64Gi"),
							Aggregator: &min,
						},
					},
				},
			},
		},
	}
}

func TestNpdFetcher_GetNPD(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name                string
		nodeName            string
		npd                 *nodev1alpha1.NodeProfileDescriptor
		includeNPDInAPI     bool // whether to include the npd object in the fake API server
		preWriteCheckpoint  bool // whether to write a checkpoint before calling GetNPD
		simulateLoadFailure bool // whether to replace loader with failingLoader to force checkpoint fallback
		expiredCheckpoint   bool // whether the pre-written checkpoint should be expired
	}{
		{
			name:               "get npd",
			nodeName:           "node-1",
			npd:                generateTestNPD("node-1"),
			includeNPDInAPI:    true,
			preWriteCheckpoint: false,
		},
		{
			name:                "get npd from checkpoint when fetch fails",
			nodeName:            "node-ckpt",
			npd:                 generateTestNPD("node-ckpt"),
			includeNPDInAPI:     false,
			preWriteCheckpoint:  true,
			simulateLoadFailure: true,
			expiredCheckpoint:   false,
		},
		{
			name:                "checkpoint expired",
			nodeName:            "node-expired",
			npd:                 generateTestNPD("node-expired"),
			includeNPDInAPI:     false,
			preWriteCheckpoint:  true,
			simulateLoadFailure: true,
			expiredCheckpoint:   true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := &v1alpha1.CustomNodeConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name: tc.nodeName,
				},
				Status: v1alpha1.CustomNodeConfigStatus{
					KatalystCustomConfigList: []v1alpha1.TargetConfig{
						{
							ConfigName: tc.npd.Name,
							ConfigType: util.NPDGVR,
							Hash:       "fake-hash",
						},
					},
				},
			}

			// set up
			cacheTTL := 1 * time.Second
			scheme := runtime.NewScheme()
			utilruntime.Must(v1alpha1.AddToScheme(scheme))
			utilruntime.Must(nodev1alpha1.AddToScheme(scheme))

			// prepare clients: include npd in API server only if specified
			var dynamicObjs []runtime.Object
			var internalObjs []runtime.Object
			internalObjs = append(internalObjs, c)
			dynamicObjs = append(dynamicObjs, c)
			if tc.includeNPDInAPI {
				internalObjs = append(internalObjs, tc.npd)
				dynamicObjs = append(dynamicObjs, tc.npd)
			}

			clientSet := &client.GenericClientSet{
				KubeClient:     nil,
				InternalClient: internalfake.NewSimpleClientset(internalObjs...),
				DynamicClient:  dynamicfake.NewSimpleDynamicClient(scheme, dynamicObjs...),
			}
			cncFetcher := cnc.NewCachedCNCFetcher(
				&global.BaseConfiguration{NodeName: tc.nodeName},
				&metaconfig.CNCConfiguration{CustomNodeConfigCacheTTL: cacheTTL},
				clientSet.InternalClient.ConfigV1alpha1().CustomNodeConfigs())

			dir := testDir + "/TestUpdateNodeProfileDescriptor/" + tc.name
			_ = os.RemoveAll(dir)
			require.NoError(t, os.MkdirAll(dir, os.FileMode(0o755)))
			conf := generateTestConfiguration(t, dir)

			fetcherIface, err := NewNPDFetcher(clientSet, cncFetcher, conf, &metrics.DummyMetrics{})
			require.NoError(t, err)

			// type assert to concrete implementation when we need to manipulate checkpoint or loader
			fimpl, ok := fetcherIface.(*npdFetcher)
			require.True(t, ok, "fetcher should be *npdFetcher")

			if tc.preWriteCheckpoint {
				if tc.expiredCheckpoint {
					// create a checkpoint with an old timestamp (older than grace time)
					cp := NewCheckpoint(NodeProfileData{})
					// timestamp older than allowed grace time
					old := metav1.NewTime(time.Now().Add(-conf.ConfigCheckpointGraceTime - time.Minute))
					cp.SetProfile(tc.npd, old)
					err := fimpl.checkpointManager.CreateCheckpoint(npdFetcherCheckpoint, cp)
					require.NoError(t, err)
				} else {
					fimpl.writeCheckpoint(tc.npd)
				}
			}

			if tc.simulateLoadFailure {
				// replace loader to simulate remote load failure so that checkpoint is used
				fimpl.configLoader = &failingLoader{}
			}

			got, err := fimpl.GetNPD(context.Background())
			if tc.expiredCheckpoint {
				// when checkpoint expired and remote load fails, expect an error
				require.Error(t, err)
				require.Nil(t, got)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.npd, got)
			}
			// clean up
			_ = os.RemoveAll(dir)
		})
	}
}
