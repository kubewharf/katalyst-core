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
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
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
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/reporter"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/reporter/manager"
	hmadvisor "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_memory"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/metaserver/kcc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func tmpDirs() (regDir, ckDir, statDir string, err error) {
	regDir, err = ioutil.TempDir("", "reg")
	if err != nil {
		return
	}
	_ = os.MkdirAll(regDir, 0o755)
	ckDir, err = ioutil.TempDir("", "ck")
	if err != nil {
		return
	}
	_ = os.MkdirAll(ckDir, 0o755)
	statDir, err = ioutil.TempDir("", "stat")
	if err != nil {
		return
	}
	_ = os.MkdirAll(statDir, 0o755)
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
				MachineInfo: &info.MachineInfo{},
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

	headroomReporter, err := NewHeadroomReporter(metrics.DummyMetrics{}, metaServer, newTestMetaCache(t), conf, advisorStub)
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

func generateMachineConfig(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)

	tmpStateDir, err := ioutil.TempDir("", "sys-advisor-test")
	require.NoError(t, err)
	testConfiguration.GenericSysAdvisorConfiguration.StateFileDirectory = tmpStateDir

	return testConfiguration
}

func newTestMetaCache(t *testing.T) *metacache.MetaCacheImp {
	metaCache, err := metacache.NewMetaCacheImp(generateMachineConfig(t), metricspool.DummyMetricsEmitterPool{}, metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}))
	require.NoError(t, err)
	require.NotNil(t, metaCache)
	return metaCache
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
	genericPlugin, _, err := newHeadroomReporterPlugin(metrics.DummyMetrics{}, metaServer, newTestMetaCache(t), conf, advisorStub)
	require.NoError(t, err)
	require.NotNil(t, genericPlugin)
	_ = genericPlugin.Start()
	defer func() { _ = genericPlugin.Stop() }()

	setupReporterManager(t, context.Background(), regDir, conf)

	advisorStub.SetHeadroom(v1.ResourceCPU, resource.MustParse("10"))
	advisorStub.SetHeadroom(v1.ResourceMemory, resource.MustParse("10Gi"))

	time.Sleep(1 * time.Second)
}

type MockHeadroomManager struct {
	allocatable           resource.Quantity
	capacity              resource.Quantity
	numaAllocatable       map[int]resource.Quantity
	numaCapacity          map[int]resource.Quantity
	getAllocatableErr     error
	getCapacityErr        error
	getNumaAllocatableErr error
	getNumaCapacityErr    error
	name                  v1.ResourceName
	milliValue            bool
}

func (m *MockHeadroomManager) Name() v1.ResourceName {
	return m.name
}

func (m *MockHeadroomManager) MilliValue() bool {
	return m.milliValue
}

func (m *MockHeadroomManager) GetAllocatable() (resource.Quantity, error) {
	return m.allocatable, m.getAllocatableErr
}

func (m *MockHeadroomManager) GetCapacity() (resource.Quantity, error) {
	return m.capacity, m.getCapacityErr
}

func (m *MockHeadroomManager) GetNumaAllocatable() (map[int]resource.Quantity, error) {
	return m.numaAllocatable, m.getNumaAllocatableErr
}

func (m *MockHeadroomManager) GetNumaCapacity() (map[int]resource.Quantity, error) {
	return m.numaCapacity, m.getNumaCapacityErr
}

func (m *MockHeadroomManager) Run(_ context.Context) {
	return
}

func TestGetReclaimedResource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		setupMock     map[v1.ResourceName]func(*MockHeadroomManager)
		wantResources *reclaimedResource
		wantErr       bool
	}{
		{
			name: "test valid resource aggregation",
			setupMock: map[v1.ResourceName]func(*MockHeadroomManager){
				v1.ResourceCPU: func(m *MockHeadroomManager) {
					m.allocatable = resource.MustParse("10")
					m.capacity = resource.MustParse("20")
					m.numaAllocatable = map[int]resource.Quantity{0: resource.MustParse("5")}
					m.numaCapacity = map[int]resource.Quantity{0: resource.MustParse("10")}
					m.name = v1.ResourceCPU
					m.milliValue = false
				},
			},
			wantResources: &reclaimedResource{
				allocatable:     v1.ResourceList{v1.ResourceCPU: resource.MustParse("10")},
				capacity:        v1.ResourceList{v1.ResourceCPU: resource.MustParse("20")},
				numaAllocatable: map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("5")}},
				numaCapacity:    map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("10")}},
				resourceNameMap: map[v1.ResourceName]v1.ResourceName{
					v1.ResourceCPU: v1.ResourceCPU,
				},
				milliValue: map[v1.ResourceName]bool{
					v1.ResourceCPU: false,
				},
			},
		},
		{
			name: "test error in GetAllocatable",
			setupMock: map[v1.ResourceName]func(*MockHeadroomManager){
				v1.ResourceCPU: func(m *MockHeadroomManager) {
					m.getAllocatableErr = fmt.Errorf("mock error")
				},
			},
			wantErr: true,
		},
		{
			name: "test zero allocatable handling",
			setupMock: map[v1.ResourceName]func(*MockHeadroomManager){
				v1.ResourceCPU: func(m *MockHeadroomManager) {
					m.allocatable = resource.MustParse("0")
					m.capacity = resource.MustParse("20")
					m.numaAllocatable = map[int]resource.Quantity{0: resource.MustParse("0")}
					m.numaCapacity = map[int]resource.Quantity{0: resource.MustParse("10")}
					m.name = v1.ResourceCPU
					m.milliValue = false
				},
			},
			wantResources: &reclaimedResource{
				allocatable:     v1.ResourceList{v1.ResourceCPU: resource.MustParse("0")},
				capacity:        v1.ResourceList{v1.ResourceCPU: resource.MustParse("20")},
				numaAllocatable: map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("0")}},
				numaCapacity:    map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("10")}},
				resourceNameMap: map[v1.ResourceName]v1.ResourceName{
					v1.ResourceCPU: v1.ResourceCPU,
				},
				milliValue: map[v1.ResourceName]bool{
					v1.ResourceCPU: false,
				},
			},
		},
		{
			name: "test mixed zero and non-zero allocatable",
			setupMock: map[v1.ResourceName]func(*MockHeadroomManager){
				v1.ResourceCPU: func(m *MockHeadroomManager) {
					m.allocatable = resource.MustParse("0")
					m.capacity = resource.MustParse("20")
					m.numaAllocatable = map[int]resource.Quantity{0: resource.MustParse("0")}
					m.numaCapacity = map[int]resource.Quantity{0: resource.MustParse("10")}
					m.name = v1.ResourceCPU
					m.milliValue = false
				},
				v1.ResourceMemory: func(m *MockHeadroomManager) {
					m.allocatable = resource.MustParse("15Gi")
					m.capacity = resource.MustParse("30Gi")
					m.numaAllocatable = map[int]resource.Quantity{0: resource.MustParse("7Gi")}
					m.numaCapacity = map[int]resource.Quantity{0: resource.MustParse("15Gi")}
					m.name = v1.ResourceMemory
					m.milliValue = false
				},
			},
			wantResources: &reclaimedResource{
				allocatable: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("0"),
					v1.ResourceMemory: resource.MustParse("15Gi"),
				},
				capacity: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("20"),
					v1.ResourceMemory: resource.MustParse("30Gi"),
				},
				numaAllocatable: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("0"),
						v1.ResourceMemory: resource.MustParse("7Gi"),
					},
				},
				numaCapacity: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("10"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
				},
				resourceNameMap: map[v1.ResourceName]v1.ResourceName{
					v1.ResourceCPU:    v1.ResourceCPU,
					v1.ResourceMemory: v1.ResourceMemory,
				},
				milliValue: map[v1.ResourceName]bool{
					v1.ResourceCPU:    false,
					v1.ResourceMemory: false,
				},
			},
		},
		{
			name: "test report name and resource name is different",
			setupMock: map[v1.ResourceName]func(*MockHeadroomManager){
				consts.ReclaimedResourceMilliCPU: func(m *MockHeadroomManager) {
					m.allocatable = resource.MustParse("0")
					m.capacity = resource.MustParse("20000")
					m.numaAllocatable = map[int]resource.Quantity{0: resource.MustParse("0")}
					m.numaCapacity = map[int]resource.Quantity{0: resource.MustParse("10000")}
					m.name = v1.ResourceCPU
					m.milliValue = true
				},
				consts.ReclaimedResourceMemory: func(m *MockHeadroomManager) {
					m.allocatable = resource.MustParse("15Gi")
					m.capacity = resource.MustParse("30Gi")
					m.numaAllocatable = map[int]resource.Quantity{0: resource.MustParse("7Gi")}
					m.numaCapacity = map[int]resource.Quantity{0: resource.MustParse("15Gi")}
					m.name = v1.ResourceMemory
					m.milliValue = false
				},
			},
			wantResources: &reclaimedResource{
				allocatable: v1.ResourceList{
					consts.ReclaimedResourceMilliCPU: resource.MustParse("0"),
					consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
				},
				capacity: v1.ResourceList{
					consts.ReclaimedResourceMilliCPU: resource.MustParse("20000"),
					consts.ReclaimedResourceMemory:   resource.MustParse("30Gi"),
				},
				numaAllocatable: map[int]v1.ResourceList{
					0: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("0"),
						consts.ReclaimedResourceMemory:   resource.MustParse("7Gi"),
					},
				},
				numaCapacity: map[int]v1.ResourceList{
					0: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("10000"),
						consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
					},
				},
				resourceNameMap: map[v1.ResourceName]v1.ResourceName{
					consts.ReclaimedResourceMilliCPU: v1.ResourceCPU,
					consts.ReclaimedResourceMemory:   v1.ResourceMemory,
				},
				milliValue: map[v1.ResourceName]bool{
					consts.ReclaimedResourceMilliCPU: true,
					consts.ReclaimedResourceMemory:   false,
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			headroomManagers := make(map[v1.ResourceName]manager.HeadroomManager, len(tt.setupMock))
			for resourceName, mock := range tt.setupMock {
				mockManager := &MockHeadroomManager{}
				headroomManagers[resourceName] = mockManager
				mock(mockManager)
			}

			r := &headroomReporterPlugin{
				headroomManagers: headroomManagers,
			}

			got, err := r.getReclaimedResource(false, false)
			if (err != nil) != tt.wantErr {
				t.Errorf("getReclaimedResource() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantResources != nil {
				if !reflect.DeepEqual(got.allocatable, tt.wantResources.allocatable) {
					t.Errorf("allocatable mismatch\ngot: %+v\nwant: %+v", got.allocatable, tt.wantResources.allocatable)
				}
				if !reflect.DeepEqual(got.capacity, tt.wantResources.capacity) {
					t.Errorf("capacity mismatch\ngot: %+v\nwant: %+v", got.capacity, tt.wantResources.capacity)
				}
				if !reflect.DeepEqual(got.numaAllocatable, tt.wantResources.numaAllocatable) {
					t.Errorf("numaAllocatable mismatch\ngot: %+v\nwant: %+v", got.numaAllocatable, tt.wantResources.numaAllocatable)
				}
				if !reflect.DeepEqual(got.numaCapacity, tt.wantResources.numaCapacity) {
					t.Errorf("numaCapacity mismatch\ngot: %+v\nwant: %+v", got.numaCapacity, tt.wantResources.numaCapacity)
				}
				if !reflect.DeepEqual(got.resourceNameMap, tt.wantResources.resourceNameMap) {
					t.Errorf("resourceNameMap mismatch\ngot: %+v\nwant: %+v", got.resourceNameMap, tt.wantResources.resourceNameMap)
				}
				if !reflect.DeepEqual(got.milliValue, tt.wantResources.milliValue) {
					t.Errorf("milliValue mismatch\ngot: %+v\nwant: %+v", got.milliValue, tt.wantResources.milliValue)
				}
			}
		})
	}
}

func TestReviseReclaimedResource(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		setupConf     func(*dynamic.Configuration)
		inputResource *reclaimedResource
		wantResources *reclaimedResource
		wantErr       bool
	}{
		{
			name: "test valid resource aggregation",
			inputResource: &reclaimedResource{
				allocatable:     v1.ResourceList{v1.ResourceCPU: resource.MustParse("10")},
				capacity:        v1.ResourceList{v1.ResourceCPU: resource.MustParse("20")},
				numaAllocatable: map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("5")}},
				numaCapacity:    map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("10")}},
			},
			wantResources: &reclaimedResource{
				allocatable:     v1.ResourceList{v1.ResourceCPU: resource.MustParse("10")},
				capacity:        v1.ResourceList{v1.ResourceCPU: resource.MustParse("20")},
				numaAllocatable: map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("5")}},
				numaCapacity:    map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("10")}},
			},
		},
		{
			name: "test zero allocatable handling",
			inputResource: &reclaimedResource{
				allocatable:     v1.ResourceList{v1.ResourceCPU: resource.MustParse("0")},
				capacity:        v1.ResourceList{v1.ResourceCPU: resource.MustParse("20")},
				numaAllocatable: map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("0")}},
				numaCapacity:    map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("10")}},
			},
			wantResources: &reclaimedResource{
				allocatable:     v1.ResourceList{v1.ResourceCPU: resource.MustParse("0")},
				capacity:        v1.ResourceList{v1.ResourceCPU: resource.MustParse("20")},
				numaAllocatable: map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("0")}},
				numaCapacity:    map[int]v1.ResourceList{0: {v1.ResourceCPU: resource.MustParse("10")}},
			},
		},
		{
			name: "test mixed zero and non-zero allocatable",
			setupConf: func(c *dynamic.Configuration) {
				c.MinIgnoredReclaimedResourceForReport = v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("0"),
					v1.ResourceMemory: resource.MustParse("0Gi"),
				}
			},
			inputResource: &reclaimedResource{
				allocatable: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("4"),
					v1.ResourceMemory: resource.MustParse("30Gi"),
				},
				capacity: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("8"),
					v1.ResourceMemory: resource.MustParse("30Gi"),
				},
				numaAllocatable: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("0"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
					1: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
				},
				numaCapacity: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
					1: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
				},
			},
			wantResources: &reclaimedResource{
				allocatable: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("4"),
					v1.ResourceMemory: resource.MustParse("15Gi"),
				},
				capacity: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("8"),
					v1.ResourceMemory: resource.MustParse("30Gi"),
				},
				numaAllocatable: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("0"),
						v1.ResourceMemory: resource.MustParse("0"),
					},
					1: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
				},
				numaCapacity: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
					1: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
				},
			},
		},
		{
			name: "test small numa allocatable handling",
			setupConf: func(c *dynamic.Configuration) {
				c.MinIgnoredReclaimedResourceForReport = v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("0.5"),
					v1.ResourceMemory: resource.MustParse("0Gi"),
				}
			},
			inputResource: &reclaimedResource{
				allocatable: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("0.6"),
					v1.ResourceMemory: resource.MustParse("30Gi"),
				},
				capacity: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("8"),
					v1.ResourceMemory: resource.MustParse("30Gi"),
				},
				numaAllocatable: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("0.3"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
					1: {
						v1.ResourceCPU:    resource.MustParse("0.3"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
				},
				numaCapacity: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
					1: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
				},
			},
			wantResources: &reclaimedResource{
				allocatable: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("0"),
					v1.ResourceMemory: resource.MustParse("0"),
				},
				capacity: v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("8"),
					v1.ResourceMemory: resource.MustParse("30Gi"),
				},
				numaAllocatable: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("0"),
						v1.ResourceMemory: resource.MustParse("0"),
					},
					1: {
						v1.ResourceCPU:    resource.MustParse("0"),
						v1.ResourceMemory: resource.MustParse("0"),
					},
				},
				numaCapacity: map[int]v1.ResourceList{
					0: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
					1: {
						v1.ResourceCPU:    resource.MustParse("4"),
						v1.ResourceMemory: resource.MustParse("15Gi"),
					},
				},
			},
		},
		{
			name: "test resource name and report name is different",
			setupConf: func(c *dynamic.Configuration) {
				c.MinIgnoredReclaimedResourceForReport = v1.ResourceList{
					v1.ResourceCPU:    resource.MustParse("0"),
					v1.ResourceMemory: resource.MustParse("0Gi"),
				}
			},
			inputResource: &reclaimedResource{
				allocatable: v1.ResourceList{
					consts.ReclaimedResourceMilliCPU: resource.MustParse("4000"),
					consts.ReclaimedResourceMemory:   resource.MustParse("30Gi"),
				},
				capacity: v1.ResourceList{
					consts.ReclaimedResourceMilliCPU: resource.MustParse("8000"),
					consts.ReclaimedResourceMemory:   resource.MustParse("30Gi"),
				},
				numaAllocatable: map[int]v1.ResourceList{
					0: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("0"),
						consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
					},
					1: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("4000"),
						consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
					},
				},
				numaCapacity: map[int]v1.ResourceList{
					0: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("4000"),
						consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
					},
					1: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("4000"),
						consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
					},
				},
				resourceNameMap: map[v1.ResourceName]v1.ResourceName{
					consts.ReclaimedResourceMilliCPU: v1.ResourceCPU,
					consts.ReclaimedResourceMemory:   v1.ResourceMemory,
				},
				milliValue: map[v1.ResourceName]bool{
					consts.ReclaimedResourceMilliCPU: true,
					consts.ReclaimedResourceMemory:   false,
				},
			},
			wantResources: &reclaimedResource{
				allocatable: v1.ResourceList{
					consts.ReclaimedResourceMilliCPU: resource.MustParse("4000"),
					consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
				},
				capacity: v1.ResourceList{
					consts.ReclaimedResourceMilliCPU: resource.MustParse("8000"),
					consts.ReclaimedResourceMemory:   resource.MustParse("30Gi"),
				},
				numaAllocatable: map[int]v1.ResourceList{
					0: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("0"),
						consts.ReclaimedResourceMemory:   resource.MustParse("0"),
					},
					1: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("4000"),
						consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
					},
				},
				numaCapacity: map[int]v1.ResourceList{
					0: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("4000"),
						consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
					},
					1: {
						consts.ReclaimedResourceMilliCPU: resource.MustParse("4000"),
						consts.ReclaimedResourceMemory:   resource.MustParse("15Gi"),
					},
				},
				resourceNameMap: map[v1.ResourceName]v1.ResourceName{
					consts.ReclaimedResourceMilliCPU: v1.ResourceCPU,
					consts.ReclaimedResourceMemory:   v1.ResourceMemory,
				},
				milliValue: map[v1.ResourceName]bool{
					consts.ReclaimedResourceMilliCPU: true,
					consts.ReclaimedResourceMemory:   false,
				},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &headroomReporterPlugin{
				dynamicConf: dynamic.NewDynamicAgentConfiguration(),
				emitter:     metrics.DummyMetrics{},
			}

			if tt.setupConf != nil {
				conf := dynamic.NewConfiguration()
				tt.setupConf(conf)
				r.dynamicConf.SetDynamicConfiguration(conf)
			}

			err := r.reviseReclaimedResource(tt.inputResource)
			if (err != nil) != tt.wantErr {
				t.Errorf("reviseReclaimedResource() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			got := tt.inputResource
			if tt.wantResources != nil {
				if !apiequality.Semantic.DeepEqual(got.allocatable, tt.wantResources.allocatable) {
					t.Errorf("allocatable mismatch\ngot: %+v\nwant: %+v", got.allocatable, tt.wantResources.allocatable)
				}
				if !apiequality.Semantic.DeepEqual(got.capacity, tt.wantResources.capacity) {
					t.Errorf("capacity mismatch\ngot: %+v\nwant: %+v", got.capacity, tt.wantResources.capacity)
				}
				if !apiequality.Semantic.DeepEqual(got.numaAllocatable, tt.wantResources.numaAllocatable) {
					t.Errorf("numaAllocatable mismatch\ngot: %+v\nwant: %+v", got.numaAllocatable, tt.wantResources.numaAllocatable)
				}
				if !apiequality.Semantic.DeepEqual(got.numaCapacity, tt.wantResources.numaCapacity) {
					t.Errorf("numaCapacity mismatch\ngot: %+v\nwant: %+v", got.numaCapacity, tt.wantResources.numaCapacity)
				}
				if !reflect.DeepEqual(got.resourceNameMap, tt.wantResources.resourceNameMap) {
					t.Errorf("resourceNameMap mismatch\ngot: %+v\nwant: %+v", got.resourceNameMap, tt.wantResources.resourceNameMap)
				}
				if !reflect.DeepEqual(got.milliValue, tt.wantResources.milliValue) {
					t.Errorf("milliValue mismatch\ngot: %+v\nwant: %+v", got.milliValue, tt.wantResources.milliValue)
				}
			}
		})
	}
}

func newCPUFG() *advisorsvc.FeatureGate {
	return &advisorsvc.FeatureGate{
		Name: feature_cpu.NegotiationFeatureGateCPUHeadroomReporting,
		Type: finders.FeatureGateTypeCPU,
	}
}

func newMemoryFG() *advisorsvc.FeatureGate {
	return &advisorsvc.FeatureGate{
		Name: feature_memory.NegotiationFeatureGateMemoryHeadroomReporting,
		Type: finders.FeatureGateTypeMemory,
	}
}

func newMockHeadroomManager(name v1.ResourceName, milliValue bool, alloc, cap string) *MockHeadroomManager {
	return &MockHeadroomManager{
		name:            name,
		milliValue:      milliValue,
		allocatable:     resource.MustParse(alloc),
		capacity:        resource.MustParse(cap),
		numaAllocatable: map[int]resource.Quantity{0: resource.MustParse(alloc)},
		numaCapacity:    map[int]resource.Quantity{0: resource.MustParse(cap)},
	}
}

func getOrInit(m map[string]map[string]*advisorsvc.FeatureGate, key string) map[string]*advisorsvc.FeatureGate {
	if v, ok := m[key]; ok {
		return v
	}
	return map[string]*advisorsvc.FeatureGate{}
}

func TestShouldSkipReporting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		fgs        map[string]*advisorsvc.FeatureGate
		nilReader  bool
		wantCPU    bool
		wantMemory bool
	}{
		{
			name:       "nil metaReader returns no skip",
			nilReader:  true,
			wantCPU:    false,
			wantMemory: false,
		},
		{
			name:       "empty feature gates returns no skip",
			fgs:        map[string]*advisorsvc.FeatureGate{},
			wantCPU:    false,
			wantMemory: false,
		},
		{
			name: "cpu feature gate only",
			fgs: map[string]*advisorsvc.FeatureGate{
				feature_cpu.NegotiationFeatureGateCPUHeadroomReporting: newCPUFG(),
			},
			wantCPU:    true,
			wantMemory: false,
		},
		{
			name: "memory feature gate only",
			fgs: map[string]*advisorsvc.FeatureGate{
				feature_memory.NegotiationFeatureGateMemoryHeadroomReporting: newMemoryFG(),
			},
			wantCPU:    false,
			wantMemory: true,
		},
		{
			name: "both feature gates present",
			fgs: map[string]*advisorsvc.FeatureGate{
				feature_cpu.NegotiationFeatureGateCPUHeadroomReporting:       newCPUFG(),
				feature_memory.NegotiationFeatureGateMemoryHeadroomReporting: newMemoryFG(),
			},
			wantCPU:    true,
			wantMemory: true,
		},
		{
			name: "unrelated feature gate ignored",
			fgs: map[string]*advisorsvc.FeatureGate{
				"other_fg": {Name: "other_fg"},
			},
			wantCPU:    false,
			wantMemory: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := &headroomReporterPlugin{}
			if !tt.nilReader {
				mc := metacache.NewDummyMetaCacheImp()
				if len(tt.fgs) > 0 {
					byType := map[string]map[string]*advisorsvc.FeatureGate{}
					for k, fg := range tt.fgs {
						byType[fg.Type] = getOrInit(byType, fg.Type)
						byType[fg.Type][k] = fg
					}
					for fgType, group := range byType {
						require.NoError(t, mc.SetSupportedWantedFeatureGates(fgType, group))
					}
				}
				r.metaReader = mc
			}

			skipCPU, skipMemory, err := r.shouldSkipReporting()
			require.NoError(t, err)
			assert.Equal(t, tt.wantCPU, skipCPU)
			assert.Equal(t, tt.wantMemory, skipMemory)
		})
	}
}

func TestGetReclaimedResource_SkipFlags(t *testing.T) {
	t.Parallel()

	buildPlugin := func() *headroomReporterPlugin {
		return &headroomReporterPlugin{
			headroomManagers: map[v1.ResourceName]manager.HeadroomManager{
				consts.ReclaimedResourceMilliCPU: newMockHeadroomManager(v1.ResourceCPU, true, "0", "20000"),
				consts.ReclaimedResourceMemory:   newMockHeadroomManager(v1.ResourceMemory, false, "15Gi", "30Gi"),
			},
		}
	}

	t.Run("no skip returns both resources", func(t *testing.T) {
		t.Parallel()
		got, err := buildPlugin().getReclaimedResource(false, false)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Contains(t, got.resourceNameMap, consts.ReclaimedResourceMilliCPU)
		assert.Contains(t, got.resourceNameMap, consts.ReclaimedResourceMemory)
	})

	t.Run("skip cpu drops cpu only", func(t *testing.T) {
		t.Parallel()
		got, err := buildPlugin().getReclaimedResource(true, false)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.NotContains(t, got.resourceNameMap, consts.ReclaimedResourceMilliCPU)
		assert.Contains(t, got.resourceNameMap, consts.ReclaimedResourceMemory)
		assert.NotContains(t, got.allocatable, consts.ReclaimedResourceMilliCPU)
		assert.Contains(t, got.allocatable, consts.ReclaimedResourceMemory)
	})

	t.Run("skip memory drops memory only", func(t *testing.T) {
		t.Parallel()
		got, err := buildPlugin().getReclaimedResource(false, true)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Contains(t, got.resourceNameMap, consts.ReclaimedResourceMilliCPU)
		assert.NotContains(t, got.resourceNameMap, consts.ReclaimedResourceMemory)
	})

	t.Run("skip both returns nil", func(t *testing.T) {
		t.Parallel()
		got, err := buildPlugin().getReclaimedResource(true, true)
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

func TestGetReportContent_SkipReporting(t *testing.T) {
	t.Parallel()

	newPlugin := func(fgs map[string]*advisorsvc.FeatureGate) *headroomReporterPlugin {
		mc := metacache.NewDummyMetaCacheImp()
		byType := map[string]map[string]*advisorsvc.FeatureGate{}
		for k, fg := range fgs {
			if _, ok := byType[fg.Type]; !ok {
				byType[fg.Type] = map[string]*advisorsvc.FeatureGate{}
			}
			byType[fg.Type][k] = fg
		}
		for fgType, group := range byType {
			_ = mc.SetSupportedWantedFeatureGates(fgType, group)
		}

		dynConf := dynamic.NewDynamicAgentConfiguration()
		return &headroomReporterPlugin{
			headroomManagers: map[v1.ResourceName]manager.HeadroomManager{
				consts.ReclaimedResourceMilliCPU: newMockHeadroomManager(v1.ResourceCPU, true, "1000", "20000"),
				consts.ReclaimedResourceMemory:   newMockHeadroomManager(v1.ResourceMemory, false, "1Gi", "30Gi"),
			},
			metaReader:  mc,
			dynamicConf: dynConf,
			emitter:     metrics.DummyMetrics{},
		}
	}

	t.Run("both feature gates set returns empty response", func(t *testing.T) {
		t.Parallel()
		r := newPlugin(map[string]*advisorsvc.FeatureGate{
			feature_cpu.NegotiationFeatureGateCPUHeadroomReporting:       newCPUFG(),
			feature_memory.NegotiationFeatureGateMemoryHeadroomReporting: newMemoryFG(),
		})

		resp, err := r.GetReportContent(context.Background(), &v1alpha1.Empty{})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Empty(t, resp.Content)
	})

	t.Run("no feature gate reports both", func(t *testing.T) {
		t.Parallel()
		r := newPlugin(map[string]*advisorsvc.FeatureGate{})

		resp, err := r.GetReportContent(context.Background(), &v1alpha1.Empty{})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.NotEmpty(t, resp.Content)
	})

	t.Run("only cpu feature gate still reports memory", func(t *testing.T) {
		t.Parallel()
		r := newPlugin(map[string]*advisorsvc.FeatureGate{
			feature_cpu.NegotiationFeatureGateCPUHeadroomReporting: newCPUFG(),
		})

		resp, err := r.GetReportContent(context.Background(), &v1alpha1.Empty{})
		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.NotEmpty(t, resp.Content)
	})
}
