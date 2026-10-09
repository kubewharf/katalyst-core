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

package cpu

import (
	"context"
	"flag"
	"io/ioutil"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agiledragon/gomonkey/v2"
	info "github.com/google/cadvisor/info/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	metric_consts "github.com/kubewharf/katalyst-core/pkg/consts"
	pkgconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	metricspool "github.com/kubewharf/katalyst-core/pkg/metrics/metrics-pool"
	cgroupcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
	resourcepkg "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

// registerKlogFlagsOn registers klog's flags on the provided FlagSet. It is the
// instance-level seam: production passes flag.CommandLine; tests pass a fresh
// FlagSet so repeated registrations never collide on the global command line.
//
// This only bridges klog's flags onto a local FlagSet (a read-only registration:
// flag.Value.String is invoked to capture defaults). It deliberately does NOT
// Parse --v, because parsing would call klog.Level.Set and mutate the
// process-global verbosity Level, which other parallel tests read via InitFlags.
// Keeping every klog access read-only here means no test goroutine writes the
// global Level, so the tests stay t.Parallel with no synchronization.
func registerKlogFlagsOn(fs *flag.FlagSet) {
	klog.InitFlags(fs)
}

// TestInitKlogVerbosityForTestIsIdempotent guards against reintroducing the
// "flag redefined: add_dir_header" panic: repeatedly registering klog flags on
// fresh FlagSets must not panic, and must never bridge klog flags onto the
// process-global flag.CommandLine. The test does not parse --v, so it does not
// mutate package-global klog verbosity; it is safe to run in parallel.
func TestInitKlogVerbosityForTestIsIdempotent(t *testing.T) {
	t.Parallel()

	snapshotGlobalFlags := func() map[string]struct{} {
		names := make(map[string]struct{})
		flag.CommandLine.VisitAll(func(f *flag.Flag) {
			names[f.Name] = struct{}{}
		})
		return names
	}

	before := snapshotGlobalFlags()

	// Repeatedly register klog flags on fresh FlagSets (as `go test -count=N` or
	// parallel tests would) and verify no panic and the local set gets a --v.
	const calls = 32
	for i := 0; i < calls; i++ {
		fs := flag.NewFlagSet("klog-test", flag.ContinueOnError)
		registerKlogFlagsOn(fs)
		require.NotNil(t, fs.Lookup("v"), "klog must register a --v flag on the local FlagSet")
	}

	require.Equal(t, before, snapshotGlobalFlags(),
		"klog test setup must not register klog flags on the global flag.CommandLine")
}

type failingProvisionAssembler struct {
	advisorUpdatedAtAssembly bool
	advisor                  *cpuResourceAdvisor
	err                      error
	beforeReturn             func()
}

func (a *failingProvisionAssembler) AssembleProvision(provisionassembler.ProvisionContext) (types.InternalCPUCalculationResult, error) {
	a.advisorUpdatedAtAssembly = a.advisor.advisorUpdated
	if a.beforeReturn != nil {
		a.beforeReturn()
	}
	return types.InternalCPUCalculationResult{}, a.err
}

type recordingProvisionAssembler struct {
	ctx provisionassembler.ProvisionContext
}

func (a *recordingProvisionAssembler) AssembleProvision(ctx provisionassembler.ProvisionContext) (types.InternalCPUCalculationResult, error) {
	a.ctx = ctx
	return types.InternalCPUCalculationResult{}, nil
}

type concurrentPoolUpdateMetaCache struct {
	metacache.MetaCache
	armed    bool
	poolName string
	poolInfo *types.PoolInfo
}

func (m *concurrentPoolUpdateMetaCache) injectConcurrentPoolUpdate() {
	if !m.armed {
		return
	}
	m.armed = false
	done := make(chan error, 1)
	go func() {
		done <- m.MetaCache.SetPoolInfo(m.poolName, m.poolInfo)
	}()
	if err := <-done; err != nil {
		panic(err)
	}
}

func (m *concurrentPoolUpdateMetaCache) RangePool(f func(string, *types.PoolInfo) bool) {
	m.MetaCache.RangePool(f)
	m.injectConcurrentPoolUpdate()
}

func (m *concurrentPoolUpdateMetaCache) RangeAndUpdatePool(f func(string, *types.PoolInfo) bool) error {
	m.injectConcurrentPoolUpdate()
	return m.MetaCache.RangeAndUpdatePool(f)
}

type staticIsolator struct {
	pods []string
}

func (i *staticIsolator) GetIsolatedPods() []string {
	return i.pods
}

type recordingHeadroomAssembler struct {
	dynamicConf *dynamic.Configuration
}

func (a *recordingHeadroomAssembler) GetHeadroom(dynamicConf *dynamic.Configuration) (resource.Quantity, map[int]resource.Quantity, error) {
	a.dynamicConf = dynamicConf
	return *resource.NewQuantity(1, resource.DecimalSI), nil, nil
}

func generateTestConfiguration(t *testing.T, checkpointDir, stateFileDir string) *config.Configuration {
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, conf)

	conf.GenericSysAdvisorConfiguration.StateFileDirectory = stateFileDir
	conf.MetaServerConfiguration.CheckpointManagerDir = checkpointDir
	conf.RestrictRefPolicy = nil

	return conf
}

// testResourcePackageManager provides deterministic empty resource packages for unit tests.
type testResourcePackageManager struct{}

func (m *testResourcePackageManager) NodeResourcePackages(ctx context.Context) (resourcepkg.NUMAResourcePackageItems, error) {
	return resourcepkg.NUMAResourcePackageItems{}, nil
}

func (m *testResourcePackageManager) ConvertNPDResourcePackages(npd *nodev1alpha1.NodeProfileDescriptor) (resourcepkg.NUMAResourcePackageItems, error) {
	return resourcepkg.NUMAResourcePackageItems{}, nil
}

func newTestCPUResourceAdvisor(t *testing.T, pods []*v1.Pod, conf *config.Configuration, mf *metric.FakeMetricsFetcher, profiles map[k8stypes.UID]spd.DummyPodServiceProfile) (*cpuResourceAdvisor, metacache.MetaCache) {
	metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, mf)
	require.NoError(t, err)

	// numa node0 cpu(s): 0-23,48-71
	// numa node1 cpu(s): 24-47,72-95
	cpuTopology, err := machine.GenerateDummyCPUTopology(96, 2, 2)
	assert.NoError(t, err)

	metaServer := &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				MachineInfo: &info.MachineInfo{
					NumCores: 96,
					Topology: []info.Node{
						{
							Id: 0,
						},
						{
							Id: 1,
						},
					},
				},
				CPUTopology: cpuTopology,
			},
			PodFetcher: &pod.PodFetcherStub{
				PodList: pods,
			},
			MetricsFetcher: mf,
		},
	}

	err = metaServer.SetServiceProfilingManager(spd.NewDummyServiceProfilingManager(profiles))
	require.NoError(t, err)

	err = metaServer.SetResourcePackageManager(&testResourcePackageManager{})
	require.NoError(t, err)

	cra := NewCPUResourceAdvisor(conf, struct{}{}, metaCache, metaServer, metrics.DummyMetrics{})
	require.NotNil(t, cra)

	// Inject per-test reclaim cgroup paths instead of mutating the process-global
	// reclaim consumer registry. The headroom assembler needs at least one
	// reclaim path to compute headroom; each advisor gets its own copy.
	if setter, ok := cra.headroomAssembler.(headroomassembler.ReclaimPathsSetter); ok {
		setter.SetReclaimCgroupPaths([]string{conf.BaseConfiguration.ReclaimRelativeRootCgroupPath}, nil)
	}

	return cra, metaCache
}

func makeContainerInfo(podUID, namespace, podName, containerName, qoSLevel, ownerPoolName string, annotations map[string]string,
	topologyAwareAssignments types.TopologyAwareAssignment, cpu ...float64,
) *types.ContainerInfo {
	req, limit := 0., 0.
	if len(cpu) == 1 {
		req, limit = cpu[0], cpu[0]
	} else if len(cpu) == 2 {
		req, limit = cpu[0], cpu[1]
	}

	return &types.ContainerInfo{
		PodUID:                           podUID,
		PodNamespace:                     namespace,
		PodName:                          podName,
		ContainerName:                    containerName,
		ContainerIndex:                   0,
		Labels:                           nil,
		Annotations:                      annotations,
		QoSLevel:                         qoSLevel,
		CPURequest:                       req,
		CPULimit:                         limit,
		MemoryRequest:                    0,
		MemoryLimit:                      0,
		RampUp:                           false,
		OriginOwnerPoolName:              ownerPoolName,
		OwnerPoolName:                    ownerPoolName,
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: topologyAwareAssignments,
		ContainerType:                    v1alpha1.ContainerType_MAIN,
	}
}

func TestSnapshotRegionAssignmentsCapturesRampUpAtCycleStart(t *testing.T) {
	t.Parallel()

	metaCache := metacache.NewDummyMetaCacheImp()
	require.NoError(t, metaCache.AddContainer("pod", "main", &types.ContainerInfo{
		PodUID:        "pod",
		ContainerName: "main",
		RampUp:        true,
		RegionNames:   sets.NewString(),
	}))
	advisor := &cpuResourceAdvisor{metaCache: metaCache}

	snapshot := advisor.snapshotRegionAssignments()
	// A non-NUMA-binding ramp-up container resolves to the global domain (-1).
	require.NotNil(t, snapshot.rampUpDomains, "cycle-start snapshot must carry the RampUp domain decision")
	require.True(t, snapshot.rampUpDomains.Has(commonstate.FakedNUMAID),
		"non-binding ramp-up must resolve to the global domain")
	require.Equal(t, 1, snapshot.rampUpDomains.Len(), "non-binding ramp-up must not leak onto real NUMAs")

	container, ok := metaCache.GetContainerInfo("pod", "main")
	require.True(t, ok)
	container.RampUp = false
	require.NoError(t, metaCache.SetContainerInfo("pod", "main", container))
	require.True(t, snapshot.rampUpDomains.Has(commonstate.FakedNUMAID),
		"cycle-start RampUp domain decision must be immutable")
}

func TestSnapshotRegionAssignmentsCapturesSteadyExclusiveNUMAs(t *testing.T) {
	t.Parallel()

	metaCache := metacache.NewDummyMetaCacheImp()
	require.NoError(t, metaCache.AddContainer("steady-exclusive", "main", &types.ContainerInfo{
		PodUID:        "steady-exclusive",
		ContainerName: "main",
		QoSLevel:      consts.PodAnnotationQoSLevelDedicatedCores,
		Annotations: map[string]string{
			consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
			consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
		},
		RampUp: false,
		TopologyAwareAssignments: types.TopologyAwareAssignment{
			0: machine.NewCPUSet(0, 1),
		},
		RegionNames: sets.NewString(),
	}))
	require.NoError(t, metaCache.AddContainer("ramp-up", "main", &types.ContainerInfo{
		PodUID:        "ramp-up",
		ContainerName: "main",
		RampUp:        true,
		RegionNames:   sets.NewString(),
		TopologyAwareAssignments: types.TopologyAwareAssignment{
			1: machine.NewCPUSet(2, 3),
		},
	}))
	advisor := &cpuResourceAdvisor{metaCache: metaCache}

	snapshot := advisor.snapshotRegionAssignments()

	// The ramp-up container has no numa-binding annotation, so it resolves to the
	// global domain and must not claim a real NUMA.
	require.True(t, snapshot.rampUpDomains.Has(commonstate.FakedNUMAID))
	require.Equal(t, 1, snapshot.rampUpDomains.Len())
	require.Equal(t, sets.NewInt(0), snapshot.steadyExclusiveNUMAs)
}

func TestAssembleProvisionPassesCycleRampUpSnapshot(t *testing.T) {
	t.Parallel()

	assembler := &recordingProvisionAssembler{}
	advisor := &cpuResourceAdvisor{provisionAssembler: assembler}
	dynamicConf := dynamic.NewConfiguration()

	_, err := advisor.assembleProvision(
		dynamicConf,
		true,
		sets.NewInt(commonstate.FakedNUMAID, 7),
		cputypes.ReclaimConstraintNone,
		nil,
		nil,
	)
	require.NoError(t, err)
	require.ElementsMatch(t, []int{commonstate.FakedNUMAID, 7}, assembler.ctx.RampUpDomains)
}

func TestCPUResourceAdvisorUsesActiveHardTargetForRegionEssentials(t *testing.T) {
	t.Parallel()

	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	shareRegion := &region.QoSRegionShare{
		QoSRegionBase: region.NewQoSRegionBase(
			"share", commonstate.PoolNameShare, "", configapi.QoSRegionTypeShare,
			conf, struct{}{}, true, false, nil, nil, nil,
		),
	}
	shareRegion.SetBindingNumas(machine.NewCPUSet(0))

	advisor := &cpuResourceAdvisor{
		numaAvailable:          map[int]int{0: 32},
		reservedForReclaim:     map[int]int{0: 2},
		rampUpReclaimCPUSetCap: map[int]int{0: 6},
		numRegionsPerNuma:      map[int]int{0: 1},
	}

	// The reservation floor is the pure steady reserve (2); the ramp-up hard cap
	// (6) is the desired tracked by the optional per-scope ceiling and must NOT
	// widen the floor. So the region keeps 32-2=30 available and the reserved
	// amount reported back is the steady 2, not the ramp-up target 6.
	require.Equal(t, float64(30), advisor.getRegionMaxRequirement(shareRegion, nil, nil))
	require.Equal(t, float64(2), advisor.getRegionReservedForReclaim(shareRegion))
}

func TestAdvisorUpdateHardPartitionNUMAAvailability(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		hardPartition   bool
		numaAvailable   map[int]int
		wantReservedMap map[int]int
	}{
		{
			name:            "hard partition does not make sysadvisor reject one empty NUMA",
			hardPartition:   true,
			numaAvailable:   map[int]int{0: 0, 1: 8},
			wantReservedMap: map[int]int{0: 2, 1: 2},
		},
		{
			name:            "hard partition does not make sysadvisor reject negative availability",
			hardPartition:   true,
			numaAvailable:   map[int]int{0: -1, 1: 8},
			wantReservedMap: map[int]int{0: 2, 1: 2},
		},
		{
			name:            "hard partition reserves two CPUs on every NUMA",
			hardPartition:   true,
			numaAvailable:   map[int]int{0: 2, 1: 2},
			wantReservedMap: map[int]int{0: 2, 1: 2},
		},
		{
			name:            "soft partition preserves legacy behavior",
			hardPartition:   false,
			numaAvailable:   map[int]int{0: 0, 1: 8},
			wantReservedMap: map[int]int{0: 2, 1: 2},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
			dynamicConf := conf.GetDynamicConfiguration()
			dynamicConf.EnableRampUpReclaimHardPartition = tt.hardPartition
			dynamicConf.InitialRampUpReclaimCPUSetRatio = 0

			mf := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
			advisor, metaCache := newTestCPUResourceAdvisor(t, nil, conf, mf, nil)

			assignments := make(map[int]machine.CPUSet, len(tt.numaAvailable))
			for numaID, available := range tt.numaAvailable {
				numaCPUs := advisor.metaServer.NUMAToCPUs[numaID].ToSliceInt()
				reservedCount := len(numaCPUs) - available
				reservedCPUs := append([]int(nil), numaCPUs...)
				if reservedCount > len(reservedCPUs) {
					otherNUMA := (numaID + 1) % advisor.metaServer.NumNUMANodes
					reservedCPUs = append(reservedCPUs, advisor.metaServer.NUMAToCPUs[otherNUMA].ToSliceInt()...)
				}
				assignments[numaID] = machine.NewCPUSet(reservedCPUs[:reservedCount]...)
			}
			require.NoError(t, metaCache.SetPoolInfo(commonstate.PoolNameReserve, &types.PoolInfo{
				PoolName:                 commonstate.PoolNameReserve,
				TopologyAwareAssignments: assignments,
			}))

			result, err := advisor.UpdateAndGetAdvice(context.Background())
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantReservedMap, advisor.reservedForReclaim)
		})
	}
}

func TestAdvisorUpdate(t *testing.T) {
	t.Parallel()

	// No klog verbosity override here: setting --v would mutate the process-global
	// klog Level, which other parallel tests read via InitFlags. The test asserts
	// on advisor state/metrics, not on log verbosity, so it runs at default level.

	type containerMetricItem struct {
		pod       string
		container string
		value     float64
	}

	type numaMetricItem struct {
		numaID int
		name   string
		value  float64
	}

	type cpuMetricItem struct {
		cpuID int
		name  string
		value float64
	}

	type cgroupMetricItem struct {
		cgroupPath string
		name       string
		value      float64
	}

	tests := []struct {
		name                          string
		preUpdate                     bool
		pools                         map[string]*types.PoolInfo
		containers                    []*types.ContainerInfo
		pods                          []*v1.Pod
		nodeEnableReclaim             bool
		headroomAssembler             types.CPUHeadroomAssemblerName
		headroomPolicies              map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName
		podProfiles                   map[k8stypes.UID]spd.DummyPodServiceProfile
		wantInternalCalculationResult types.InternalCPUCalculationResult
		wantErr                       bool
		wantHeadroom                  resource.Quantity
		wantHeadroomErr               bool
		containerMetrics              []containerMetricItem
		numaMetricItems               []numaMetricItem
		cpuMetricItems                []cpuMetricItem
		cgroupMetricItems             []cgroupMetricItem
	}{
		{
			name:                          "missing_reserve_pool",
			pools:                         map[string]*types.PoolInfo{},
			wantInternalCalculationResult: types.InternalCPUCalculationResult{},
			wantErr:                       true,
			wantHeadroom:                  resource.Quantity{},
		},
		{
			name: "provision:reserve_pool_only",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameReclaim: {-1: {Size: 94, Quota: -1}},
				},
			},
			headroomPolicies: map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName{
				configapi.QoSRegionTypeDedicated: {types.CPUHeadroomPolicyNone},
			},
			wantHeadroom:    resource.Quantity{},
			wantHeadroomErr: false,
		},
		{
			name: "provision:single_small_share_pool",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 4),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameShare:   {-1: {Size: 8, Quota: -1}},
					commonstate.PoolNameReclaim: {-1: {Size: 86, Quota: -1}},
				},
			},
			wantHeadroom: resource.Quantity{},
		},
		{
			name: "provision:single_large_share_pool",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 100),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameShare:   {-1: {Size: 90, Quota: -1}},
					commonstate.PoolNameReclaim: {-1: {Size: 4, Quota: -1}},
				},
			},
			wantHeadroom: resource.Quantity{},
		},
		{
			name: "provision:multi_small_share_pools",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					},
				},
				"batch": {
					PoolName: "batch",
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("26"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1"),
						1: machine.MustParse("25"),
					}, 4),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, "batch", nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("2"),
						1: machine.MustParse("26"),
					}, 6),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameShare:   {-1: {Size: 6, Quota: -1}},
					"batch":                     {-1: {Size: 8, Quota: -1}},
					commonstate.PoolNameReclaim: {-1: {Size: 80, Quota: -1}},
				},
			},
			wantHeadroom: resource.Quantity{},
		},
		{
			name: "provision:multi_large_share_pools",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-5,48-52"),
						1: machine.MustParse("25-29,72-76"),
					},
				},
				"batch": {
					PoolName: "batch",
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-12,48-60"),
						1: machine.MustParse("25-36,72-84"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-5,48-52"),
						1: machine.MustParse("25-29,72-76"),
					}, 100),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, "batch", nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-12,48-60"),
						1: machine.MustParse("25-36,72-84"),
					}, 200),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameShare:   {-1: {Size: 30, Quota: -1}},
					"batch":                     {-1: {Size: 60, Quota: -1}},
					commonstate.PoolNameReclaim: {-1: {Size: 4, Quota: -1}},
				},
			},
			wantHeadroom: resource.Quantity{},
		},
		{
			name: "provision:single_dedicated_numa_exclusive",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("94-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
			},
			nodeEnableReclaim: true,
			headroomAssembler: types.CPUHeadroomAssemblerDedicated,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameReclaim: {0: {Size: 0, Quota: -1}, -1: {Size: 47, Quota: -1}},
				},
				PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{
					commonstate.PoolNameReclaim: {
						0: {
							"uid1": {
								"c1": 4,
							},
						},
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(4, resource.DecimalSI),
			cgroupMetricItems: []cgroupMetricItem{
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUUsageCgroup,
					value:      2.8,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUQuotaCgroup,
					value:      -1,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUPeriodCgroup,
					value:      100000,
				},
				{
					cgroupPath: "/kubepods/besteffort-0",
					name:       pkgconsts.MetricCPUUsageCgroup,
					value:      2.8,
				},
				{
					cgroupPath: "/kubepods/besteffort-0",
					name:       pkgconsts.MetricCPUQuotaCgroup,
					value:      -1,
				},
				{
					cgroupPath: "/kubepods/besteffort-0",
					name:       pkgconsts.MetricCPUPeriodCgroup,
					value:      100000,
				},
			},
			cpuMetricItems: []cpuMetricItem{
				{
					cpuID: 70,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 71,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 94,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 95,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
			},
		},
		{
			name: "provision:single_dedicated_numa_exclusive with invalid headroom policy",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("94-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
			},
			nodeEnableReclaim: true,
			headroomAssembler: types.CPUHeadroomAssemblerDedicated,
			headroomPolicies: map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName{
				configapi.QoSRegionTypeDedicated: {types.CPUHeadroomPolicyNonReclaim},
			},
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameReclaim: {0: {Size: 0, Quota: -1}, -1: {Size: 47, Quota: -1}},
				},
				PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{
					commonstate.PoolNameReclaim: {
						0: {
							"uid1": {
								"c1": 4,
							},
						},
					},
				},
			},
			wantHeadroomErr: false,
			wantHeadroom:    *resource.NewQuantity(4, resource.DecimalSI),
			cgroupMetricItems: []cgroupMetricItem{
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUUsageCgroup,
					value:      2.8,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUQuotaCgroup,
					value:      -1,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUPeriodCgroup,
					value:      100000,
				},
				{
					cgroupPath: "/kubepods/besteffort-0",
					name:       pkgconsts.MetricCPUUsageCgroup,
					value:      2.8,
				},
				{
					cgroupPath: "/kubepods/besteffort-0",
					name:       pkgconsts.MetricCPUQuotaCgroup,
					value:      -1,
				},
				{
					cgroupPath: "/kubepods/besteffort-0",
					name:       pkgconsts.MetricCPUPeriodCgroup,
					value:      100000,
				},
			},
			cpuMetricItems: []cpuMetricItem{
				{
					cpuID: 70,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 71,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 94,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 95,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
			},
		},
		{
			name: "single_dedicated_numa_exclusive pod un-reclaimed",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("94-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
			},
			nodeEnableReclaim: true,
			podProfiles:       map[k8stypes.UID]spd.DummyPodServiceProfile{"uid1": {PerformanceLevel: spd.PerformanceLevelPoor, Score: 0}},
			headroomAssembler: types.CPUHeadroomAssemblerDedicated,
			headroomPolicies: map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName{
				configapi.QoSRegionTypeShare:     {types.CPUHeadroomPolicyCanonical},
				configapi.QoSRegionTypeDedicated: {types.CPUHeadroomPolicyNUMADedicated},
			},
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameReclaim: {0: {Size: 0, Quota: -1}, -1: {Size: 47, Quota: -1}},
				},
				PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{
					commonstate.PoolNameReclaim: {
						0: {
							"uid1": {
								"c1": 2,
							},
						},
					},
				},
			},
			// NUMA 0 is dedicated-bound, leaving only NUMA 1 in the global
			// non-binding reclaim branch. With non-overlap reclaim supply, headroom
			// uses the exclusive reclaim cpuset size (94-95) directly.
			wantHeadroom: *resource.NewQuantity(2, resource.DecimalSI),
			cgroupMetricItems: []cgroupMetricItem{
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUUsageCgroup,
					value:      2.8,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUQuotaCgroup,
					value:      -1,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUPeriodCgroup,
					value:      100000,
				},
			},
			cpuMetricItems: []cpuMetricItem{
				{
					cpuID: 70,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 71,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 94,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 95,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
			},
		},
		{
			name: "single_dedicated_numa_exclusive pod with performance score",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("94-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
			},
			nodeEnableReclaim: true,
			podProfiles:       map[k8stypes.UID]spd.DummyPodServiceProfile{"uid1": {PerformanceLevel: spd.PerformanceLevelPerfect, Score: 50}},
			headroomAssembler: types.CPUHeadroomAssemblerDedicated,
			headroomPolicies: map[configapi.QoSRegionType][]types.CPUHeadroomPolicyName{
				configapi.QoSRegionTypeShare:     {types.CPUHeadroomPolicyCanonical},
				configapi.QoSRegionTypeDedicated: {types.CPUHeadroomPolicyNUMADedicated},
			},
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {
						-1: {Size: 2, Quota: -1},
					},
					commonstate.PoolNameReclaim: {
						0:  {Size: 0, Quota: -1},
						-1: {Size: 47, Quota: -1},
					},
				},
				PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{
					commonstate.PoolNameReclaim: {
						0: {
							"uid1": {
								"c1": 4,
							},
						},
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(4, resource.DecimalSI),
			cgroupMetricItems: []cgroupMetricItem{
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUUsageCgroup,
					value:      2.8,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUQuotaCgroup,
					value:      -1,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUPeriodCgroup,
					value:      100000,
				},
			},
			cpuMetricItems: []cpuMetricItem{
				{
					cpuID: 70,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 71,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 94,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 95,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
			},
		},
		{
			name: "dedicated_numa_exclusive_&_share",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.MustParse("25-30"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("94-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-18,48-65"),
					}, 36),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						1: machine.MustParse("25-28"),
					}, 4),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {
						-1: {Size: 2, Quota: -1},
					},
					commonstate.PoolNameShare: {
						-1: {Size: 6, Quota: -1},
					},
					commonstate.PoolNameReclaim: {
						0:  {Size: 0, Quota: -1},
						-1: {Size: 41, Quota: -1},
					},
				},
				PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{
					commonstate.PoolNameReclaim: {
						0: {
							"uid1": {
								"c1": 4,
							},
						},
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(4, resource.DecimalSI),
			cgroupMetricItems: []cgroupMetricItem{
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUUsageCgroup,
					value:      2.8,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUQuotaCgroup,
					value:      -1,
				},
				{
					cgroupPath: "/kubepods/besteffort",
					name:       pkgconsts.MetricCPUPeriodCgroup,
					value:      100000,
				},
			},
			cpuMetricItems: []cpuMetricItem{
				{
					cpuID: 70,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 71,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 94,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
				{
					cpuID: 95,
					name:  pkgconsts.MetricCPUUsageRatio,
					value: 0.3,
				},
			},
		},
		{
			name: "dedicated_numa_exclusive_&_share_disable_reclaim",
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						1: machine.MustParse("25-30"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("70-71"),
						1: machine.MustParse("31-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelDedicatedCores, commonstate.PoolNameDedicated,
					map[string]string{
						consts.PodAnnotationMemoryEnhancementNumaBinding:   consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						consts.PodAnnotationMemoryEnhancementNumaExclusive: consts.PodAnnotationMemoryEnhancementNumaExclusiveEnable,
					},
					map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
					}, 36),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						1: machine.MustParse("25-28"),
					}, 4),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
			},
			nodeEnableReclaim: false,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {
						-1: {Size: 2, Quota: -1},
					},
					commonstate.PoolNameShare: {
						-1: {Size: 45, Quota: -1},
					},
					commonstate.PoolNameReclaim: {
						0:  {Size: 0, Quota: -1},
						-1: {Size: 2, Quota: -1},
					},
				},
				PoolOverlapPodContainerInfo: map[string]map[int]map[string]map[string]int{
					commonstate.PoolNameReclaim: {
						0: {
							"uid1": {
								"c1": 2,
							},
						},
					},
				},
			},
			wantHeadroom: *resource.NewQuantity(0, resource.DecimalSI),
		},
		{
			name:      "provision:single_large_share_pool&isolation_within_limits",
			preUpdate: true,
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-21,48-69"),
						1: machine.MustParse("25-45,72-93"),
					},
				},
				commonstate.PoolNameReclaim: {
					PoolName: commonstate.PoolNameReclaim,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("22-23,70-71"),
						1: machine.MustParse("46-47,94-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 2),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 20),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 30),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameShare:   {-1: {Size: 82, Quota: -1}},
					commonstate.PoolNameReclaim: {-1: {Size: 10, Quota: -1}},
					"isolation-pod1":            {-1: {Size: 2, Quota: -1}},
				},
			},
			wantHeadroom: resource.Quantity{},
			containerMetrics: []containerMetricItem{
				{
					pod:       "uid1",
					container: "c1",
					value:     50,
				},
				{
					pod:       "uid2",
					container: "c2",
					value:     8,
				},
				{
					pod:       "uid3",
					container: "c3",
					value:     4,
				},
			},
			numaMetricItems: []numaMetricItem{
				{
					numaID: 0,
					name:   pkgconsts.MetricMemLatencyReadNuma,
					value:  80,
				},
				{
					numaID: 1,
					name:   pkgconsts.MetricMemLatencyReadNuma,
					value:  80,
				},
				{
					numaID: 0,
					name:   pkgconsts.MetricMemLatencyWriteNuma,
					value:  200,
				},
				{
					numaID: 1,
					name:   pkgconsts.MetricMemLatencyWriteNuma,
					value:  200,
				},
				{
					numaID: 0,
					name:   pkgconsts.MetricMemAMDL3MissLatencyNuma,
					value:  400,
				},
				{
					numaID: 1,
					name:   pkgconsts.MetricMemAMDL3MissLatencyNuma,
					value:  400,
				},
			},
		},
		{
			name:      "provision:single_large_share_pool&isolation_exceed_limit",
			preUpdate: true,
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 8),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 20),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 2, 8),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameShare:   {-1: {Size: 88, Quota: -1}},
					commonstate.PoolNameReclaim: {-1: {Size: 6, Quota: -1}},
				},
			},
			wantHeadroom: resource.Quantity{},
			containerMetrics: []containerMetricItem{
				{
					pod:       "uid1",
					container: "c1",
					value:     50,
				},
				{
					pod:       "uid2",
					container: "c2",
					value:     8,
				},
				{
					pod:       "uid3",
					container: "c3",
					value:     4,
				},
				{
					pod:       "uid4",
					container: "c4",
					value:     70,
				},
			},
		},
		{
			name:      "provision:ignore_share_cores_without_request",
			preUpdate: true,
			pools: map[string]*types.PoolInfo{
				commonstate.PoolNameReserve: {
					PoolName: commonstate.PoolNameReserve,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("0"),
						1: machine.MustParse("24"),
					},
				},
				commonstate.PoolNameShare: {
					PoolName: commonstate.PoolNameShare,
					TopologyAwareAssignments: map[int]machine.CPUSet{
						0: machine.MustParse("1-23,48-71"),
						1: machine.MustParse("25-47,72-95"),
					},
				},
			},
			containers: []*types.ContainerInfo{
				makeContainerInfo("uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 8),
				makeContainerInfo("uid2", "default", "pod2", "c2", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 20),
				makeContainerInfo("uid3", "default", "pod3", "c3", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 30),
				makeContainerInfo("uid4", "default", "pod4", "c4", consts.PodAnnotationQoSLevelSharedCores, commonstate.PoolNameShare, nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}, 2, 8),
				makeContainerInfo("uid5", "default", "pod5", "c5", consts.PodAnnotationQoSLevelSharedCores, "", nil,
					map[int]machine.CPUSet{
						0: machine.MustParse("1-22,48-70"),
						1: machine.MustParse("25-46,72-94"),
					}),
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod1",
						Namespace: "default",
						UID:       "uid1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod2",
						Namespace: "default",
						UID:       "uid2",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod3",
						Namespace: "default",
						UID:       "uid3",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod4",
						Namespace: "default",
						UID:       "uid4",
					},
				},
			},
			nodeEnableReclaim: true,
			wantInternalCalculationResult: types.InternalCPUCalculationResult{
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReserve: {-1: {Size: 2, Quota: -1}},
					commonstate.PoolNameShare:   {-1: {Size: 88, Quota: -1}},
					commonstate.PoolNameReclaim: {-1: {Size: 6, Quota: -1}},
				},
			},
			wantHeadroom: resource.Quantity{},
			containerMetrics: []containerMetricItem{
				{
					pod:       "uid1",
					container: "c1",
					value:     50,
				},
				{
					pod:       "uid2",
					container: "c2",
					value:     8,
				},
				{
					pod:       "uid3",
					container: "c3",
					value:     4,
				},
				{
					pod:       "uid4",
					container: "c4",
					value:     70,
				},
				{
					pod:       "uid5",
					container: "c5",
					value:     7,
				},
			},
		},
	}

	// The production headroom assembler filters reclaim cgroup paths through
	// cgroupcommon.GetExistingRelativeCgroupPaths, which returns an empty slice
	// for the fake paths used in tests and causes GetReclaimMetricsMulti to error.
	// Install a single function-level pass-through patch so all parallel
	// subtests see it; per-subtest teardown would race with siblings.
	existingPathsPatches := gomonkey.ApplyFunc(cgroupcommon.GetExistingRelativeCgroupPaths, func(paths ...string) []string {
		return paths
	})
	defer t.Cleanup(func() {
		existingPathsPatches.Reset()
	})

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			now := time.Now()

			ckDir, err := ioutil.TempDir("", "checkpoint-TestAdvisorUpdate")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(ckDir) }()

			sfDir, err := ioutil.TempDir("", "statefile")
			require.NoError(t, err)
			defer func() { _ = os.RemoveAll(sfDir) }()

			mf := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)

			conf := generateTestConfiguration(t, ckDir, sfDir)
			if tt.headroomAssembler != "" {
				conf.CPUAdvisorConfiguration.HeadroomAssembler = tt.headroomAssembler
			}
			if len(tt.headroomPolicies) != 0 {
				conf.CPUAdvisorConfiguration.HeadroomPolicies = tt.headroomPolicies
			}
			conf.IsolatedMaxResourceRatio = 0.3
			conf.IsolationLockInThreshold = 1
			conf.IsolationLockOutPeriodSecs = 30
			conf.GetDynamicConfiguration().RegionIndicatorTargetConfiguration = map[configapi.QoSRegionType][]configv1alpha1.IndicatorTargetConfiguration{
				configapi.QoSRegionTypeShare: {
					{
						Name:   workloadapis.ServiceSystemIndicatorNameCPUSchedWait,
						Target: 460,
					},
					{
						Name:   workloadapis.ServiceSystemIndicatorNameCPUUsageRatio,
						Target: 0.8,
					},
					{
						Name:   workloadapis.ServiceSystemIndicatorNameMemoryAccessReadLatency,
						Target: 80,
					},
					{
						Name:   workloadapis.ServiceSystemIndicatorNameMemoryAccessWriteLatency,
						Target: 200,
					},
					{
						Name:   workloadapis.ServiceSystemIndicatorNameMemoryL3MissLatency,
						Target: 400,
					},
				},
				configapi.QoSRegionTypeDedicated: {
					{
						Name:   workloadapis.ServiceSystemIndicatorNameCPI,
						Target: 1.4,
					},
					{
						Name:   workloadapis.ServiceSystemIndicatorNameCPUUsageRatio,
						Target: 0.55,
					},
				},
			}

			advisor, metaCache := newTestCPUResourceAdvisor(t, tt.pods, conf, mf, tt.podProfiles)
			advisor.conf.GetDynamicConfiguration().EnableReclaim = tt.nodeEnableReclaim

			if len(tt.containerMetrics) > 0 {
				advisor.conf.IsolationDisabled = false
				for _, m := range tt.containerMetrics {
					mf.SetContainerMetric(m.pod, m.container, metric_consts.MetricCPUNrRunnableContainer, utilmetric.MetricData{Value: m.value, Time: &now})
				}
			}

			for _, metric := range tt.numaMetricItems {
				mf.SetNumaMetric(metric.numaID, metric.name, utilmetric.MetricData{Value: metric.value, Time: &now})
			}

			for _, metric := range tt.cpuMetricItems {
				mf.SetCPUMetric(metric.cpuID, metric.name, utilmetric.MetricData{Value: metric.value, Time: &now})
			}

			for _, metric := range tt.cgroupMetricItems {
				mf.SetCgroupMetric(metric.cgroupPath, metric.name, utilmetric.MetricData{Value: metric.value, Time: &now})
			}

			for poolName, poolInfo := range tt.pools {
				_ = metaCache.SetPoolInfo(poolName, poolInfo)
			}
			for _, c := range tt.containers {
				_ = metaCache.SetContainerInfo(c.PodUID, c.ContainerName, c)
			}

			var wg sync.WaitGroup
			wg.Add(1)
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				defer wg.Done()
				advisor.Run(ctx)
			}()

			// if preUpdate is enabled, trigger an empty update firstly
			if tt.preUpdate {
				_, err := advisor.UpdateAndGetAdvice(ctx)
				if tt.wantErr {
					assert.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}

			// trigger advisor update
			advisorRespRaw, err := advisor.UpdateAndGetAdvice(ctx)
			if tt.wantErr {
				assert.Error(t, err)
				cancel()
				wg.Wait()
				return
			}
			require.NoError(t, err)
			advisorResp := advisorRespRaw.(*types.CPUAdvice).CalculationResult

			// check provision
			if !reflect.DeepEqual(tt.wantInternalCalculationResult, types.InternalCPUCalculationResult{}) {
				resp := make(map[string]map[int]types.CPUResource)
				for pool := range advisorResp.PoolEntries {
					if strings.HasPrefix(pool, "isolation") && len(pool) > 15 {
						resp[pool[:14]] = advisorResp.PoolEntries[pool]
					} else {
						resp[pool] = advisorResp.PoolEntries[pool]
					}
				}
				if !reflect.DeepEqual(tt.wantInternalCalculationResult.PoolEntries, resp) {
					t.Errorf("cpu provision\nexpected: %+v,\nactual: %+v", tt.wantInternalCalculationResult, advisorResp)
				}
			}

			// check headroom
			if !reflect.DeepEqual(tt.wantHeadroom, resource.Quantity{}) {
				headroom, _, err := advisor.GetHeadroom()
				if tt.wantHeadroomErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
					if !reflect.DeepEqual(tt.wantHeadroom.MilliValue(), headroom.MilliValue()) {
						t.Errorf("headroom\nexpected: %+v\nactual: %+v", tt.wantHeadroom, headroom)
					}
				}
			}

			cancel()
			wg.Wait()
		})
	}
}

func TestAdvisorUpdateFailureRollsBackTentativeMetadata(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
	mf := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{}).(*metric.FakeMetricsFetcher)
	advisor, metaCache := newTestCPUResourceAdvisor(t, nil, conf, mf, nil)

	require.NoError(t, metaCache.SetPoolInfo(commonstate.PoolNameReserve, &types.PoolInfo{
		PoolName: commonstate.PoolNameReserve,
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.MustParse("0"),
			1: machine.MustParse("24"),
		},
	}))
	require.NoError(t, metaCache.SetPoolInfo(commonstate.PoolNameShare, &types.PoolInfo{
		PoolName:    commonstate.PoolNameShare,
		RegionNames: sets.NewString("committed-share-region"),
	}))
	require.NoError(t, metaCache.SetContainerInfo("uid1", "c1", makeContainerInfo(
		"uid1", "default", "pod1", "c1", consts.PodAnnotationQoSLevelSharedCores,
		commonstate.PoolNameShare, nil, nil, 1, 100,
	)))
	require.NoError(t, metaCache.RangeAndUpdateContainer(func(_, _ string, ci *types.ContainerInfo) bool {
		ci.RegionNames = sets.NewString("committed-share-region")
		ci.Isolated = false
		return true
	}))

	committedConf := conf.GetDynamicConfiguration()
	advisor.lastCommittedConf = committedConf
	advisor.advisorUpdated = true
	advisor.isolator = &staticIsolator{pods: []string{"uid1"}}
	concurrentPoolInfo := &types.PoolInfo{
		PoolName: commonstate.PoolNameShare,
		TopologyAwareAssignments: types.TopologyAwareAssignment{
			0: machine.MustParse("4-7"),
		},
		OriginalTopologyAwareAssignments: types.TopologyAwareAssignment{
			0: machine.MustParse("4-11"),
		},
		RegionNames: sets.NewString("tentative-share-region"),
	}
	concurrentMetaCache := &concurrentPoolUpdateMetaCache{
		MetaCache: metaCache,
		poolName:  commonstate.PoolNameShare,
		poolInfo:  concurrentPoolInfo,
	}
	advisor.metaCache = concurrentMetaCache
	failingAssembler := &failingProvisionAssembler{
		advisor: advisor,
		err:     assert.AnError,
		beforeReturn: func() {
			concurrentMetaCache.armed = true
		},
	}
	advisor.provisionAssembler = failingAssembler

	_, err := advisor.update()
	require.Error(t, err)
	require.True(t, failingAssembler.advisorUpdatedAtAssembly,
		"isolation fallback must not invalidate the previously committed advice before the final failure")
	require.False(t, advisor.advisorUpdated)
	require.Same(t, committedConf, advisor.lastCommittedConf)
	require.Empty(t, advisor.regionMap)

	container, ok := metaCache.GetContainerInfo("uid1", "c1")
	require.True(t, ok)
	require.Equal(t, sets.NewString("committed-share-region"), container.RegionNames)
	require.False(t, container.Isolated)

	pool, ok := metaCache.GetPoolInfo(commonstate.PoolNameShare)
	require.True(t, ok)
	require.Equal(t, sets.NewString("committed-share-region"), pool.RegionNames)
	require.Equal(t, concurrentPoolInfo.TopologyAwareAssignments, pool.TopologyAwareAssignments)
	require.Equal(t, concurrentPoolInfo.OriginalTopologyAwareAssignments, pool.OriginalTopologyAwareAssignments)

	advisor.isolator = &staticIsolator{}
	failingAssembler.err = nil
	nextConf := dynamic.NewConfiguration()
	conf.SetDynamicConfiguration(nextConf)
	_, err = advisor.update()
	require.NoError(t, err)
	require.Same(t, nextConf, advisor.lastCommittedConf)
	require.NotEmpty(t, advisor.regionMap)
	require.NotContains(t, advisor.regionMap, "committed-share-region")
	container, ok = metaCache.GetContainerInfo("uid1", "c1")
	require.True(t, ok)
	require.Len(t, container.RegionNames, 1)
	for regionName := range container.RegionNames {
		require.Contains(t, advisor.regionMap, regionName)
	}
}

func TestAdvisorGetHeadroomUsesLastSuccessfulDynamicConfiguration(t *testing.T) {
	t.Parallel()

	committedConf := dynamic.NewConfiguration()
	currentConf := dynamic.NewConfiguration()
	assembler := &recordingHeadroomAssembler{}
	conf, err := options.NewOptions().Config()
	require.NoError(t, err)
	conf.SetDynamicConfiguration(currentConf)
	advisor := &cpuResourceAdvisor{
		conf:              conf,
		advisorUpdated:    true,
		lastCommittedConf: committedConf,
		headroomAssembler: assembler,
	}

	_, _, err = advisor.GetHeadroom()
	require.NoError(t, err)
	require.Same(t, committedConf, assembler.dynamicConf)
}

func TestGetIsolatedContainerRegions(t *testing.T) {
	t.Parallel()

	c1_1 := &types.ContainerInfo{PodUID: "p1", ContainerName: "c1_1"}
	c1_2 := &types.ContainerInfo{PodUID: "p1", ContainerName: "c1_2"}
	c2 := &types.ContainerInfo{PodUID: "p2", ContainerName: "c2"}
	c3_1 := &types.ContainerInfo{PodUID: "p3", ContainerName: "c3_1"}
	c3_2 := &types.ContainerInfo{PodUID: "p3", ContainerName: "c3_2"}

	conf, _ := options.NewOptions().Config()

	r1 := &region.QoSRegionShare{
		QoSRegionBase: region.NewQoSRegionBase("r1", "", "", configapi.QoSRegionTypeIsolation,
			conf, struct{}{}, false, false, nil, nil, nil),
	}
	_ = r1.AddContainer(c1_1)
	_ = r1.AddContainer(c1_2)

	r2 := &region.QoSRegionShare{
		QoSRegionBase: region.NewQoSRegionBase("r2", "", "", configapi.QoSRegionTypeShare,
			conf, struct{}{}, false, false, nil, nil, nil),
	}
	_ = r2.AddContainer(c2)

	r3 := &region.QoSRegionShare{
		QoSRegionBase: region.NewQoSRegionBase("r3", "", "", configapi.QoSRegionTypeDedicated,
			conf, struct{}{}, false, true, nil, nil, nil),
	}
	_ = r3.AddContainer(c3_1)
	_ = r3.AddContainer(c3_2)

	advisor := &cpuResourceAdvisor{
		regionMap: map[string]region.QoSRegion{
			"r1": r1,
			"r2": r2,
			"r3": r3,
		},
	}

	f := func(c *types.ContainerInfo) []string {
		rs, err := advisor.getContainerRegions(c, configapi.QoSRegionTypeIsolation)
		assert.NoError(t, err)
		var res []string
		for _, r := range rs {
			res = append(res, r.Name())
		}
		return res
	}
	assert.ElementsMatch(t, []string{"r1"}, f(c1_1))
	assert.ElementsMatch(t, []string{"r1"}, f(c1_2))
	assert.ElementsMatch(t, []string{}, f(c2))
	assert.ElementsMatch(t, []string{}, f(c3_1))
	assert.ElementsMatch(t, []string{}, f(c3_2))
}

func TestAssignShareContainerToRegionsSkipsNUMABindingRampUpWithoutOwner(t *testing.T) {
	t.Parallel()

	conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
	conf.GenericSysAdvisorConfiguration.EnableShareCoresNumaBinding = true
	advisor := &cpuResourceAdvisor{conf: conf}
	container := &types.ContainerInfo{
		PodUID:        "snb-ramp-up",
		ContainerName: "main",
		QoSLevel:      consts.PodAnnotationQoSLevelSharedCores,
		RampUp:        true,
		Annotations: map[string]string{
			consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
		},
		TopologyAwareAssignments: map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
		},
	}

	regions, err := advisor.assignShareContainerToRegions(container)

	require.NoError(t, err)
	require.Nil(t, regions)
}

// recordedInt64Metric captures a single StoreInt64 call together with the tags
// it was emitted with, so tag-level assertions (e.g. pool_type) are possible.
type recordedInt64Metric struct {
	name  string
	value int64
	tags  map[string]string
}

// recordingMetricEmitter records the latest int64 value stored per metric name
// so the metrics helper can be asserted directly in unit tests. It additionally
// keeps the full per-call history (with tags) for tag-level assertions.
type recordingMetricEmitter struct {
	metrics.DummyMetrics
	int64Values map[string]int64
	records     []recordedInt64Metric
}

func newRecordingMetricEmitter() *recordingMetricEmitter {
	return &recordingMetricEmitter{int64Values: make(map[string]int64)}
}

func (r *recordingMetricEmitter) StoreInt64(name string, value int64, _ metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	r.int64Values[name] = value
	tagMap := make(map[string]string, len(tags))
	for _, tag := range tags {
		tagMap[tag.Key] = tag.Val
	}
	r.records = append(r.records, recordedInt64Metric{name: name, value: value, tags: tagMap})
	return nil
}

func (r *recordingMetricEmitter) Int64Values() map[string]int64 {
	out := make(map[string]int64, len(r.int64Values))
	for name, value := range r.int64Values {
		out[name] = value
	}
	return out
}

// poolTypeByName returns the pool_type tag emitted for the given metric name and
// name tag value, matching the first record found.
func (r *recordingMetricEmitter) poolTypeByName(metricName, nameTag string) (string, bool) {
	for _, rec := range r.records {
		if rec.name == metricName && rec.tags["name"] == nameTag {
			return rec.tags["pool_type"], true
		}
	}
	return "", false
}

func TestEmitDefaultShareBackfillMetrics(t *testing.T) {
	t.Parallel()

	t.Run("enabled emits all", func(t *testing.T) {
		t.Parallel()

		emitter := newRecordingMetricEmitter()
		diagnostics := types.DefaultShareBackfillDiagnostics{
			Enabled: true, AllocatableBudget: 190, ReserveSize: 2, FixedPoolSize: 8,
			RawReclaimSize: 186, FinalReclaimSize: 56, ReleasedReclaimSize: 130,
			DedicatedSize: 0, IsolationSize: 0, CustomSharedSize: 0, SNBSize: 0,
			ExclusiveNUMASize: 0, PinnedCPUSize: 0,
			DefaultShareBeforeBackfill: 4, DefaultShareBackfilled: 130,
			DefaultShareFinal: 134, UnassignedNonReclaimSize: 0,
		}
		emitDefaultShareBackfillMetrics(emitter, diagnostics)
		expected := map[string]int64{
			"cpu_advisor_machine_allocatable":           190,
			"cpu_advisor_reserve_size":                  2,
			"cpu_advisor_fixed_pool_size":               8,
			"cpu_advisor_raw_reclaim_size":              186,
			"cpu_advisor_final_reclaim_size":            56,
			"cpu_advisor_reclaim_clamped_size":          130,
			"cpu_advisor_dedicated_size":                0,
			"cpu_advisor_isolation_size":                0,
			"cpu_advisor_custom_shared_size":            0,
			"cpu_advisor_snb_size":                      0,
			"cpu_advisor_exclusive_numa_size":           0,
			"cpu_advisor_pinned_cpu_size":               0,
			"cpu_advisor_default_share_before_backfill": 4,
			"cpu_advisor_default_share_backfilled":      130,
			"cpu_advisor_default_share_final":           134,
			"cpu_advisor_unassigned_non_reclaim_size":   0,
		}
		require.Equal(t, expected, emitter.Int64Values())
	})

	t.Run("disabled no-op", func(t *testing.T) {
		t.Parallel()

		emitter := newRecordingMetricEmitter()
		diagnostics := types.DefaultShareBackfillDiagnostics{
			Enabled: false, AllocatableBudget: 190, ReserveSize: 2, FixedPoolSize: 8,
			RawReclaimSize: 186, FinalReclaimSize: 56, ReleasedReclaimSize: 130,
			DefaultShareBeforeBackfill: 4, DefaultShareBackfilled: 130,
			DefaultShareFinal: 134,
		}
		emitDefaultShareBackfillMetrics(emitter, diagnostics)
		require.Len(t, emitter.Int64Values(), 0)
	})
}

// TestEmitMetricsLabelsDedicatedPoolType verifies that pool entries persisted
// under a dedicated region's pod uid are labeled pool_type=dedicated in the
// katalyst.cpu_advisor_pool_size series, while share/reclaim pools keep their
// canonical pool_type derived from commonstate.GetPoolType.
func TestEmitMetricsLabelsDedicatedPoolType(t *testing.T) {
	t.Parallel()

	conf, _ := options.NewOptions().Config()

	const dedicatedPodUID = "dedicated-pod-uid"

	dedicatedRegion := &region.QoSRegionShare{
		QoSRegionBase: region.NewQoSRegionBase("dedicated-region", "", "", configapi.QoSRegionTypeDedicated,
			conf, struct{}{}, true, true, nil, nil, nil),
	}
	require.NoError(t, dedicatedRegion.AddContainer(&types.ContainerInfo{
		PodUID:        dedicatedPodUID,
		ContainerName: "main",
	}))

	shareRegion := &region.QoSRegionShare{
		QoSRegionBase: region.NewQoSRegionBase("share-region", commonstate.PoolNameShare, "", configapi.QoSRegionTypeShare,
			conf, struct{}{}, false, false, nil, nil, nil),
	}

	emitter := newRecordingMetricEmitter()
	cra := &cpuResourceAdvisor{
		emitter: emitter,
		period:  time.Second,
		regionMap: map[string]region.QoSRegion{
			"dedicated-region": dedicatedRegion,
			"share-region":     shareRegion,
		},
	}

	calculationResult := types.InternalCPUCalculationResult{
		PoolEntries: map[string]map[int]types.CPUResource{
			// dedicated allocation persisted keyed by pod uid.
			dedicatedPodUID: {
				0: {Size: 8, Quota: 8},
			},
			commonstate.PoolNameShare: {
				-1: {Size: 4, Quota: 4},
			},
			commonstate.PoolNameReclaim: {
				-1: {Size: 2, Quota: 2},
			},
		},
	}

	cra.emitMetrics(calculationResult)

	dedicatedType, ok := emitter.poolTypeByName(metricCPUAdvisorPoolSize, dedicatedPodUID)
	require.True(t, ok, "dedicated pool size metric should be emitted")
	require.Equal(t, commonstate.PoolNameDedicated, dedicatedType)

	shareType, ok := emitter.poolTypeByName(metricCPUAdvisorPoolSize, commonstate.PoolNameShare)
	require.True(t, ok, "share pool size metric should be emitted")
	require.Equal(t, commonstate.PoolNameShare, shareType)

	reclaimType, ok := emitter.poolTypeByName(metricCPUAdvisorPoolSize, commonstate.PoolNameReclaim)
	require.True(t, ok, "reclaim pool size metric should be emitted")
	require.Equal(t, commonstate.PoolNameReclaim, reclaimType)
}

// new8NUMAMetaServer builds a MetaServer with 8 real NUMA nodes (0..7) and a
// populated CPUDetails map, so ramp-up domain derivation validates container
// placements against a realistic multi-NUMA topology instead of the lightweight
// no-topology unit-test path.
func new8NUMAMetaServer() *metaserver.MetaServer {
	const cpusPerNuma = 8
	cpuDetails := machine.CPUDetails{}
	cpuID := 0
	for numa := 0; numa < 8; numa++ {
		for i := 0; i < cpusPerNuma; i++ {
			cpuDetails[cpuID] = machine.CPUTopoInfo{NUMANodeID: numa, CoreID: cpuID}
			cpuID++
		}
	}
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: &machine.CPUTopology{
					NumCPUs:      64,
					NumCores:     64,
					NumSockets:   1,
					NumNUMANodes: 8,
					CPUDetails:   cpuDetails,
				},
			},
		},
	}
}

func TestCPUAdvisorRampUpDomainsCrossDomain8NUMAs(t *testing.T) {
	t.Parallel()

	// fixture builds a fresh 8-NUMA advisor + metaCache + a real provision
	// assembler wired to the given per-NUMA hard-partition cap map. Each subtest
	// gets its own fixture so ramp-up containers cannot leak across scenarios.
	newFixture := func(t *testing.T, caps map[int]int) (*cpuResourceAdvisor, metacache.MetaCache) {
		conf := generateTestConfiguration(t, t.TempDir(), t.TempDir())
		dynConf := conf.GetDynamicConfiguration()
		dynConf.EnableReclaim = true
		dynConf.EnableRampUpReclaimHardPartition = true

		mf := metric.NewFakeMetricsFetcher(metrics.DummyMetrics{})
		metaCache, err := metacache.NewMetaCacheImp(conf, metricspool.DummyMetricsEmitterPool{}, mf)
		require.NoError(t, err)

		cra := NewCPUResourceAdvisor(conf, struct{}{}, metaCache, new8NUMAMetaServer(), metrics.DummyMetrics{})

		// The assembler no longer captures these maps at construction: assembleProvision
		// deep-copies them from the advisor's own bookkeeping into the per-cycle Snapshot.
		cra.regionMap = map[string]region.QoSRegion{}
		cra.reservedForReclaim = map[int]int{0: 2, 1: 2, 2: 2, 3: 2, 4: 2, 5: 2, 6: 2, 7: 2}
		cra.rampUpReclaimCPUSetCap = caps
		cra.numaAvailable = map[int]int{0: 8, 1: 8, 2: 8, 3: 8, 4: 8, 5: 8, 6: 8, 7: 8}
		cra.nonBindingNumas = machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6, 7)
		// The overlap flags are resolved per-cycle from dynamicConf (the single
		// authority); there is no persistent advisor field to set.
		dynConf.AllowSharedCoresOverlapReclaimedCores = false
		dynConf.DisableDedicatedCoresOverlapReclaimedCores = true
		return cra, metaCache
	}

	addNUMABindingRampUp := func(t *testing.T, metaCache metacache.MetaCache, podUID string, numa int) {
		require.NoError(t, metaCache.AddContainer(podUID, "main", &types.ContainerInfo{
			PodUID:                   podUID,
			ContainerName:            "main",
			RampUp:                   true,
			Annotations:              map[string]string{consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable},
			TopologyAwareAssignments: types.TopologyAwareAssignment{numa: machine.NewCPUSet(0)},
		}))
	}
	addNonBindingRampUp := func(t *testing.T, metaCache metacache.MetaCache, podUID string) {
		require.NoError(t, metaCache.AddContainer(podUID, "main", &types.ContainerInfo{
			PodUID:        podUID,
			ContainerName: "main",
			RampUp:        true,
		}))
	}
	assemble := func(t *testing.T, cra *cpuResourceAdvisor) types.InternalCPUCalculationResult {
		snap := cra.snapshotRegionAssignments()
		result, err := cra.assembleProvision(
			cra.conf.GetDynamicConfiguration(),
			snap.rampUpDomains.Len() > 0,
			snap.rampUpDomains,
			cputypes.ReclaimConstraintNone,
			nil,
			nil,
		)
		require.NoError(t, err)
		return result
	}

	t.Run("A_SourceOnNUMA7FloorsOnlyItsOwnDomain", func(t *testing.T) {
		t.Parallel()
		cra, metaCache := newFixture(t, map[int]int{7: 8})
		addNUMABindingRampUp(t, metaCache, "rampup-numa7", 7)

		result := assemble(t, cra)

		reclaim := result.PoolEntries[commonstate.PoolNameReclaim]
		require.Contains(t, reclaim, 7, "NUMA7 must carry its local hard-partition reclaim target")
		require.Equal(t, 8, reclaim[7].Size)
		// Cross-domain closure: a NUMA7 ramp-up must not write a reclaim target onto
		// NUMA0/2/3, so their dedicated pools keep their steady share.
		for _, numa := range []int{0, 2, 3} {
			_, leaked := reclaim[numa]
			require.False(t, leaked, "NUMA%d must not inherit NUMA7's ramp-up reclaim target", numa)
		}
	})

	t.Run("B_GlobalSourceDoesNotWritePerNUMATarget", func(t *testing.T) {
		t.Parallel()
		// A global (non-binding) ramp-up writes no per-NUMA hard-partition cap, so
		// the cap map stays empty and no real NUMA gets a per-NUMA reclaim target.
		cra, metaCache := newFixture(t, map[int]int{})
		addNonBindingRampUp(t, metaCache, "rampup-global")

		result := assemble(t, cra)

		// The live-reclaim floor for the global domain is applied on every real NUMA
		// in the cpu server output layer (covered by server-side tests); the advisor
		// itself must not pin a real NUMA's dedicated target via a per-NUMA cap.
		for numa, entry := range result.PoolEntries[commonstate.PoolNameReclaim] {
			require.NotEqual(t, 8, entry.Size, "global ramp-up must not write a per-NUMA reclaim target, got NUMA%d=%d", numa, entry.Size)
		}
	})

	t.Run("C_NoRampUpIsBitCompatibleWithLegacyBehavior", func(t *testing.T) {
		t.Parallel()
		cra, _ := newFixture(t, map[int]int{})

		assemble(t, cra)
	})
}

// newProductionLikeSMT2MetaServer builds an 8-NUMA node with 24 logical CPUs per
// NUMA (12 physical cores, SMT2), mirroring the production node's shape.
func newProductionLikeSMT2MetaServer() *metaserver.MetaServer {
	const (
		numNUMAs     = 8
		cpusPerNuma  = 24
		cpusPerCore  = 2
		coresPerNuma = cpusPerNuma / cpusPerCore
	)
	cpuDetails := machine.CPUDetails{}
	cpuID := 0
	for numa := 0; numa < numNUMAs; numa++ {
		for core := 0; core < coresPerNuma; core++ {
			coreID := numa*coresPerNuma + core
			for sib := 0; sib < cpusPerCore; sib++ {
				cpuDetails[cpuID] = machine.CPUTopoInfo{NUMANodeID: numa, CoreID: coreID}
				cpuID++
			}
		}
	}
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			KatalystMachineInfo: &machine.KatalystMachineInfo{
				CPUTopology: &machine.CPUTopology{
					NumCPUs:      numNUMAs * cpusPerNuma,
					NumCores:     numNUMAs * coresPerNuma,
					NumSockets:   1,
					NumNUMANodes: numNUMAs,
					CPUDetails:   cpuDetails,
				},
			},
		},
	}
}

// TestUpdateRampUpReclaimCPUSetCapGlobalDomainAggregatesBeforeRounding drives the
// real advisor cap derivation on a production-shaped node (binding NUMAs 0/3/7,
// non-binding 1/2/4/5/6 = 120 logical CPUs, SMT2) and checks that a global
// (faked) ramp-up derives the whole-core aggregate target 120*0.2=24 and distributes
// it back across the non-binding NUMAs as complete cores, without touching the
// binding NUMAs.
func TestUpdateRampUpReclaimCPUSetCapGlobalDomainAggregatesBeforeRounding(t *testing.T) {
	t.Parallel()

	bindingNUMAs := sets.NewInt(0, 3, 7)
	nonBinding := machine.NewCPUSet(1, 2, 4, 5, 6)

	cra := &cpuResourceAdvisor{
		metaServer:         newProductionLikeSMT2MetaServer(),
		nonBindingNumas:    nonBinding,
		reservedForReclaim: map[int]int{0: 2, 1: 2, 2: 2, 3: 2, 4: 2, 5: 2, 6: 2, 7: 2},
	}
	dynConf := dynamic.NewConfiguration()
	dynConf.EnableReclaim = true
	dynConf.EnableRampUpReclaimHardPartition = true
	dynConf.InitialRampUpReclaimCPUSetRatio = 0.2

	domains := sets.NewInt(commonstate.FakedNUMAID)
	require.NoError(t, cra.updateRampUpReclaimCPUSetCap(dynConf, true, sets.NewInt(), domains))

	cap := cra.rampUpReclaimCPUSetCap
	require.NotEmpty(t, cap, "global ramp-up must produce a per-NUMA cap map")

	total := 0
	for numaID, size := range cap {
		require.False(t, bindingNUMAs.Has(numaID),
			"global ramp-up must not write onto binding NUMA %d", numaID)
		require.Equal(t, 0, size%2, "NUMA%d share must be whole-core, got %d", numaID, size)
		total += size
	}
	require.Equal(t, 24, total, "aggregate target must be whole-core-floor(120*0.2)=24")

	// Deterministic water-fill: 5 equal 24-CPU members, baseline 2 each (sum 10),
	// remaining 14 CPUs = 7 cores -> first two NUMAs get an extra core each.
	require.Equal(t, map[int]int{1: 6, 2: 6, 4: 4, 5: 4, 6: 4}, cap)
}

// TestUpdateRampUpReclaimCPUSetCapGlobalAndPartitionCoexist checks the global and
// a NUMA-binding domain derive independent targets in the same cycle: the global
// aggregate lands on non-binding NUMAs, while the binding NUMA keeps its own local
// target and is not pulled into the global distribution.
func TestUpdateRampUpReclaimCPUSetCapGlobalAndPartitionCoexist(t *testing.T) {
	t.Parallel()

	nonBinding := machine.NewCPUSet(1, 2, 4, 5, 6)
	cra := &cpuResourceAdvisor{
		metaServer:         newProductionLikeSMT2MetaServer(),
		nonBindingNumas:    nonBinding,
		reservedForReclaim: map[int]int{0: 2, 1: 2, 2: 2, 3: 2, 4: 2, 5: 2, 6: 2, 7: 2},
	}
	dynConf := dynamic.NewConfiguration()
	dynConf.EnableReclaim = true
	dynConf.EnableRampUpReclaimHardPartition = true
	dynConf.InitialRampUpReclaimCPUSetRatio = 0.2

	// Global domain plus a binding ramp-up on NUMA 7.
	domains := sets.NewInt(commonstate.FakedNUMAID, 7)
	require.NoError(t, cra.updateRampUpReclaimCPUSetCap(dynConf, true, sets.NewInt(), domains))

	cap := cra.rampUpReclaimCPUSetCap
	require.Contains(t, cap, 7, "NUMA7 keeps its own local ramp-up target")
	globalTotal := 0
	for numaID, size := range cap {
		if numaID == 7 {
			continue
		}
		require.False(t, sets.NewInt(0, 3, 7).Has(numaID), "global target must not touch binding NUMA %d", numaID)
		globalTotal += size
	}
	require.Equal(t, 24, globalTotal, "non-binding NUMAs still sum to the global aggregate target")
}
