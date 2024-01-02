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

package metaserver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/metaserver/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/external"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func generateTestMetaServer(clientSet *client.GenericClientSet, conf *config.Configuration) *MetaServer {
	return &MetaServer{
		MetaAgent: &agent.MetaAgent{
			PodFetcher:     &pod.PodFetcherStub{},
			NodeFetcher:    node.NewRemoteNodeFetcher(conf.NodeName, clientSet.KubeClient.CoreV1().Nodes()),
			CNRFetcher:     cnr.NewCachedCNRFetcher(conf.NodeName, conf.CNRCacheTTL, clientSet.InternalClient.NodeV1alpha1().CustomNodeResources()),
			MetricsFetcher: metric.NewMetricsFetcher(metrics.DummyMetrics{}, &pod.PodFetcherStub{}, conf),
			Conf:           conf,
		},
		ConfigurationManager:    &dynamicconfig.DummyConfigurationManager{},
		ServiceProfilingManager: &spd.DummyServiceProfilingManager{},
		ExternalManager:         &external.DummyExternalManager{},
	}
}

func TestMetaServer_Run(t *testing.T) {
	t.Parallel()

	genericClient := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}
	conf := generateTestConfiguration(t)
	meta := generateTestMetaServer(genericClient, conf)

	meta.Lock()
	assert.False(t, meta.start)
	meta.Unlock()

	go meta.Run(context.Background())

	time.Sleep(3 * time.Millisecond)

	meta.Lock()
	assert.True(t, meta.start)
	meta.Unlock()
}

func TestMetaServer_SetServiceProfilingManager(t *testing.T) {
	t.Parallel()

	genericClient := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}
	conf := generateTestConfiguration(t)
	meta := generateTestMetaServer(genericClient, conf)

	err := meta.SetServiceProfilingManager(&spd.DummyServiceProfilingManager{})
	assert.NoError(t, err)

	go meta.Run(context.Background())

	time.Sleep(3 * time.Second)

	err = meta.SetServiceProfilingManager(&spd.DummyServiceProfilingManager{})
	assert.Error(t, err)
}
