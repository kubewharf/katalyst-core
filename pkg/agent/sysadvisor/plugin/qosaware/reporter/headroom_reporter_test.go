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

package reporter

import (
	"context"
	"io/ioutil"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	kubeletconfig "k8s.io/kubernetes/pkg/kubelet/config"
	"k8s.io/kubernetes/pkg/kubelet/pluginmanager"
	plugincache "k8s.io/kubernetes/pkg/kubelet/pluginmanager/cache"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/reporter"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/metaserver/kcc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func tmpDirs() (regDir, ckDir, statDir string, err error) {
	regDir, err = ioutil.TempDir("", "reg")
	if err != nil {
		return
	}
	_ = os.MkdirAll(regDir, 0755)
	ckDir, err = ioutil.TempDir("", "ck")
	if err != nil {
		return
	}
	_ = os.MkdirAll(ckDir, 0755)
	statDir, err = ioutil.TempDir("", "stat")
	if err != nil {
		return
	}
	_ = os.MkdirAll(statDir, 0755)
	return
}

func generateTestConfiguration(t *testing.T, regDir, ckDir, stateFileDir string) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	testConfiguration.GetDynamicConfiguration().EnableReclaim = true

	testConfiguration.PluginRegistrationDir = regDir
	testConfiguration.CheckpointManagerDir = ckDir
	testConfiguration.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	testConfiguration.GenericReporterConfiguration.InnerPlugins = nil
	testConfiguration.HeadroomReporterSyncPeriod = 30 * time.Millisecond
	testConfiguration.HeadroomReporterSlidingWindowTime = 180 * time.Millisecond
	testConfiguration.CollectInterval = 30 * time.Millisecond
	testConfiguration.NodeName = "test-node"
	testConfiguration.NodeMetricReporterConfiguration.SyncPeriod = time.Millisecond * 100
	testConfiguration.NodeMetricReporterConfiguration.MetricSlidingWindowTime = time.Second
	return testConfiguration
}

func generateTestGenericClientSet(kubeObjects, internalObjects []runtime.Object) *client.GenericClientSet {
	return &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(kubeObjects...),
		InternalClient: internalfake.NewSimpleClientset(internalObjects...),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), internalObjects...),
	}
}

func generateTestMetaServer(clientSet *client.GenericClientSet, conf *config.Configuration, podList ...*v1.Pod) *metaserver.MetaServer {
	cpuTopology, _ := machine.GenerateDummyCPUTopology(16, 1, 2)
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			NodeFetcher: node.NewRemoteNodeFetcher(conf.BaseConfiguration, conf.NodeConfiguration,
				clientSet.KubeClient.CoreV1().Nodes()),
			CNRFetcher: cnr.NewCachedCNRFetcher(conf.BaseConfiguration, conf.CNRConfiguration,
				clientSet.InternalClient.NodeV1alpha1().CustomNodeResources()),
			PodFetcher:     &pod.PodFetcherStub{PodList: podList},
			MetricsFetcher: metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}),
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: cpuTopology,
			},
		},
		ConfigurationManager: &dynamicconfig.DummyConfigurationManager{},
	}
}

func setupReporterManager(t *testing.T, ctx context.Context, socketDir string, conf *config.Configuration) registration.AgentPluginHandler {
	testReporter := reporter.NewReporterManagerStub()
	m, err := fetcher.NewReporterPluginManager(testReporter, metrics.DummyMetrics{}, nil, conf)
	require.NoError(t, err)
	go m.Run(ctx)

	pluginManager := pluginmanager.NewPluginManager(
		socketDir,
		&record.FakeRecorder{},
	)

	pluginManager.AddHandler(m.GetHandlerType(), plugincache.PluginHandler(m))

	go pluginManager.Run(kubeletconfig.NewSourcesReady(func(_ sets.String) bool { return true }), ctx.Done())

	return m
}

func TestNewReclaimedResourcedReporter(t *testing.T) {
	t.Parallel()

	regDir, ckDir, statDir, err := tmpDirs()
	require.NoError(t, err)
	defer func() {
		os.RemoveAll(regDir)
		os.RemoveAll(ckDir)
		os.RemoveAll(statDir)
	}()

	conf := generateTestConfiguration(t, regDir, ckDir, statDir)
	clientSet := generateTestGenericClientSet(nil, nil)
	metaServer := generateTestMetaServer(clientSet, conf)

	advisorStub := hmadvisor.NewResourceAdvisorStub()

	headroomReporter, err := NewHeadroomReporter(metrics.DummyMetrics{}, metaServer, conf, advisorStub)
	require.NoError(t, err)
	require.NotNil(t, headroomReporter)

	ctx, cancel := context.WithCancel(context.Background())
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		headroomReporter.Run(ctx)
	}()
	time.Sleep(1 * time.Second)
	cancel()
	wg.Wait()
}

func TestReclaimedResourcedReporterWithManager(t *testing.T) {
	t.Parallel()

	regDir, ckDir, statDir, err := tmpDirs()
	require.NoError(t, err)
	defer func() {
		os.RemoveAll(regDir)
		os.RemoveAll(ckDir)
		os.RemoveAll(statDir)
	}()

	conf := generateTestConfiguration(t, regDir, ckDir, statDir)
	clientSet := generateTestGenericClientSet(nil, nil)
	metaServer := generateTestMetaServer(clientSet, conf)

	advisorStub := hmadvisor.NewResourceAdvisorStub()
	genericPlugin, err := newHeadroomReporterPlugin(metrics.DummyMetrics{}, metaServer, conf, advisorStub)
	require.NoError(t, err)
	require.NotNil(t, genericPlugin)
	_ = genericPlugin.Start()
	defer func() { _ = genericPlugin.Stop() }()

	setupReporterManager(t, context.Background(), regDir, conf)

	advisorStub.SetHeadroom(v1.ResourceCPU, resource.MustParse("10"))
	advisorStub.SetHeadroom(v1.ResourceMemory, resource.MustParse("10Gi"))

	time.Sleep(1 * time.Second)
}
