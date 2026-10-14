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

package dynamicpolicy

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"time"

	"github.com/samber/lo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	"k8s.io/kubernetes/pkg/api/v1/resource"
	maputil "k8s.io/kubernetes/pkg/util/maps"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	bulkheadmodel "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation/finders/feature_cpu"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	cgroupmgr "github.com/kubewharf/katalyst-core/pkg/util/cgroup/manager"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

const (
	cpuAdvisorHealthMonitorName     = "cpuAdvisorHealthMonitor"
	cpuAdvisorUnhealthyThreshold    = 5 * time.Minute
	cpuAdvisorHealthyThreshold      = 2 * time.Minute
	cpuAdvisorHealthyCount          = 2
	cpuAdvisorHealthMonitorInterval = 30 * time.Second
)

/* in the below, cpu-plugin works in server-mode, while cpu-advisor works in client-mode */

// cgroupResourceOps groups the per-instance cgroup / path / runtime calls used by
// the cgroup-v1 quota convergence helpers. Every field defaults to the production
// function (see defaultCgroupResourceOps). A nil internal-method field means the
// production method body runs. Tests install a fake ops on their own DynamicPolicy
// so the helpers can run under t.Parallel without monkey patching.
type cgroupResourceOps struct {
	// external calls
	checkCgroup2UnifiedMode        func() bool
	getCPUWithRelativePath         func(relCgroupPath string) (*common.CPUStats, error)
	applyCPUWithRelativePath       func(relCgroupPath string, data *common.CPUData) error
	getCPUWithAbsolutePath         func(absCgroupPath string) (*common.CPUStats, error)
	applyCPUWithAbsolutePath       func(absCgroupPath string, data *common.CPUData) error
	readDir                        func(dirname string) ([]os.DirEntry, error)
	getContainerID                 func(pod *v1.Pod, containerName string) (string, error)
	getPodAbsCgroupPath            func(subsys string, podUID string) (string, error)
	getContainerRelativeCgroupPath func(podUID string, containerID string) (string, error)
	getAbsCgroupPath               func(subsys string, suffix string) string

	// internal helper seams (nil => production method)
	getCurrentPathAllPodsDirAndMap  func(currentCgroupPath string) (map[string]*v1.Pod, []string, error)
	getPodAndRelativePath           func(currentCgroupPath, podDir string, podsPathMap map[string]*v1.Pod) (*v1.Pod, string, error)
	applyAllContainersQuota         func(pod *v1.Pod, setToLimit bool) error
	getAllContainersRelativePathMap func(pod *v1.Pod) map[string]*v1.Container
	applyAllSubCgroupQuotaToUnLimit func(containerRelativePath string) error
	checkAndApplyAllPodsQuota       func(calculationInfo *advisorsvc.CalculationInfo, bigGroupQuota int64) error
}

// defaultCgroupResourceOps binds every external field to the production function.
// Internal-method fields are left nil so the production method body runs.
func defaultCgroupResourceOps() *cgroupResourceOps {
	return &cgroupResourceOps{
		checkCgroup2UnifiedMode:        common.CheckCgroup2UnifiedMode,
		getCPUWithRelativePath:         cgroupmgr.GetCPUWithRelativePath,
		applyCPUWithRelativePath:       cgroupmgr.ApplyCPUWithRelativePath,
		getCPUWithAbsolutePath:         cgroupmgr.GetCPUWithAbsolutePath,
		applyCPUWithAbsolutePath:       cgroupmgr.ApplyCPUWithAbsolutePath,
		readDir:                        os.ReadDir,
		getContainerID:                 native.GetContainerID,
		getPodAbsCgroupPath:            common.GetPodAbsCgroupPath,
		getContainerRelativeCgroupPath: common.GetContainerRelativeCgroupPath,
		getAbsCgroupPath:               common.GetAbsCgroupPath,
	}
}

// gops returns the per-instance cgroup ops, filling production defaults on first
// use. The returned value is a pointer shared for the instance; tests replace it
// wholesale before invoking the helpers.
func (p *DynamicPolicy) gops() *cgroupResourceOps {
	if p.cgroupOps == nil {
		p.cgroupOps = defaultCgroupResourceOps()
	}
	return p.cgroupOps
}

// serveForAdvisor starts a server for cpu-advisor (as a client) to connect with
func (p *DynamicPolicy) serveForAdvisor(stopCh <-chan struct{}) {
	cpuPluginSocketDir := path.Dir(p.config.cpuPluginSocketAbsPath)

	err := general.EnsureDirectory(cpuPluginSocketDir)
	if err != nil {
		general.Errorf("ensure cpuPluginSocketDir: %s failed with error: %v", cpuPluginSocketDir, err)
		return
	}

	general.Infof("ensure cpuPluginSocketDir: %s successfully", cpuPluginSocketDir)
	if err := os.Remove(p.config.cpuPluginSocketAbsPath); err != nil && !os.IsNotExist(err) {
		general.Errorf("failed to remove %s: %v", p.config.cpuPluginSocketAbsPath, err)
		return
	}

	sock, err := net.Listen("unix", p.config.cpuPluginSocketAbsPath)
	if err != nil {
		general.Errorf("listen at socket: %s failed with err: %v", p.config.cpuPluginSocketAbsPath, err)
		return
	}
	general.Infof("listen at: %s successfully", p.config.cpuPluginSocketAbsPath)

	grpcServer := grpc.NewServer()
	advisorapi.RegisterCPUPluginServer(grpcServer, p)

	// Buffered so the Serve goroutine can always signal back without a receiver:
	// when stopCh fires we call grpcServer.Stop() and return before reading
	// exitCh, an unbuffered send would park the Serve goroutine forever.
	exitCh := make(chan struct{}, 1)
	go func() {
		general.Infof("starting cpu plugin checkpoint grpc server at socket: %s", p.config.cpuPluginSocketAbsPath)
		if err := grpcServer.Serve(sock); err != nil {
			general.Errorf("cpu plugin checkpoint grpc server crashed with error: %v at socket: %s", err, p.config.cpuPluginSocketAbsPath)
		} else {
			general.Infof("cpu plugin checkpoint grpc server at socket: %s exists normally", p.config.cpuPluginSocketAbsPath)
		}

		exitCh <- struct{}{}
	}()

	if conn, err := process.Dial(p.config.cpuPluginSocketAbsPath, 5*time.Second); err != nil {
		grpcServer.Stop()
		general.Errorf("dial check at socket: %s failed with err: %v", p.config.cpuPluginSocketAbsPath, err)
	} else {
		_ = conn.Close()
	}

	select {
	case <-exitCh:
		return
	case <-stopCh:
		grpcServer.Stop()
		<-exitCh
		return
	}
}

// GetCheckpoint works with serveForAdvisor to provide ckp for cpu-advisor
func (p *DynamicPolicy) GetCheckpoint(_ context.Context,
	req *advisorapi.GetCheckpointRequest,
) (*advisorapi.GetCheckpointResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetCheckpoint got nil req")
	}
	general.Infof("called")

	p.RLock()
	defer p.RUnlock()

	stateEntries := p.state.GetPodEntries()
	chkEntries := make(map[string]*advisorapi.AllocationEntries)
	for uid, containerEntries := range stateEntries {
		if chkEntries[uid] == nil {
			chkEntries[uid] = &advisorapi.AllocationEntries{}
		}

		for entryName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			if chkEntries[uid].Entries == nil {
				chkEntries[uid].Entries = make(map[string]*advisorapi.AllocationInfo)
			}

			chkEntries[uid].Entries[entryName] = &advisorapi.AllocationInfo{
				RampUp:        allocationInfo.RampUp,
				OwnerPoolName: allocationInfo.OwnerPoolName,
			}

			ownerPoolName := allocationInfo.GetOwnerPoolName()
			if ownerPoolName == commonstate.EmptyOwnerPoolName {
				general.Warningf("pod: %s/%s container: %s get empty owner pool name",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				if allocationInfo.CheckSideCar() {
					ownerPoolName = containerEntries.GetMainContainerPoolName()

					general.Warningf("set pod: %s/%s sidecar container: %s owner pool name: %s same to its main container",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
						ownerPoolName)
				}
			}

			chkEntries[uid].Entries[entryName].OwnerPoolName = ownerPoolName

			// not set topology-aware assignments for shared_cores,
			// since their topology-aware assignments are same to the pools they are in.
			if !allocationInfo.CheckShared() || containerEntries.IsPoolEntry() {
				chkEntries[uid].Entries[entryName].TopologyAwareAssignments = machine.ParseCPUAssignmentFormat(allocationInfo.TopologyAwareAssignments)
				chkEntries[uid].Entries[entryName].OriginalTopologyAwareAssignments = machine.ParseCPUAssignmentFormat(allocationInfo.OriginalTopologyAwareAssignments)
			}
		}
	}

	return &advisorapi.GetCheckpointResponse{
		Entries: chkEntries,
	}, nil
}

/* in the below, cpu-plugin works in client-mode, while cpu-advisor works in server-mode */

// pushCPUAdvisor pushes state info to cpu-advisor
func (p *DynamicPolicy) pushCPUAdvisor() error {
	podEntries := p.state.GetPodEntries()
	for _, entries := range podEntries {
		if entries.IsPoolEntry() {
			continue
		}

		for _, allocationInfo := range entries {
			if allocationInfo == nil {
				continue
			}

			containerType, found := pluginapi.ContainerType_value[allocationInfo.ContainerType]
			if !found {
				return fmt.Errorf("sync pod: %s/%s, container: %s to cpu advisor failed with error: containerType: %s not found",
					allocationInfo.PodNamespace, allocationInfo.PodName,
					allocationInfo.ContainerName, allocationInfo.ContainerType)
			}

			_, err := p.advisor.advisorClient.AddContainer(context.Background(), &advisorsvc.ContainerMetadata{
				PodUid:               allocationInfo.PodUid,
				PodNamespace:         allocationInfo.PodNamespace,
				PodName:              allocationInfo.PodName,
				ContainerName:        allocationInfo.ContainerName,
				ContainerType:        pluginapi.ContainerType(containerType),
				ContainerIndex:       allocationInfo.ContainerIndex,
				Labels:               maputil.CopySS(allocationInfo.Labels),
				Annotations:          maputil.CopySS(allocationInfo.Annotations),
				QosLevel:             allocationInfo.QoSLevel,
				RequestQuantity:      uint64(allocationInfo.RequestQuantity),
				RequestMilliQuantity: uint64(allocationInfo.RequestQuantity * 1000),
				UseMilliQuantity:     true,
			})
			if err != nil {
				return fmt.Errorf("sync pod: %s/%s, container: %s to cpu advisor failed with error: %v",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
			}
		}
	}

	return nil
}

func (p *DynamicPolicy) createGetAdviceRequestAtRevision() (*advisorapi.GetAdviceRequest, uint64, error) {
	stateEntries, machineState, revision := p.state.GetAdvisorStateSnapshot()
	req, err := p.createGetAdviceRequestFromSnapshot(stateEntries, machineState)
	return req, revision, err
}

func (p *DynamicPolicy) createGetAdviceRequestFromSnapshot(
	stateEntries state.PodEntries,
	machineState state.NUMANodeMap,
) (*advisorapi.GetAdviceRequest, error) {
	chkEntries := make(map[string]*advisorapi.ContainerAllocationInfoEntries)
	for uid, containerEntries := range stateEntries {
		if chkEntries[uid] == nil {
			chkEntries[uid] = &advisorapi.ContainerAllocationInfoEntries{
				Entries: make(map[string]*advisorapi.ContainerAllocationInfo),
			}
		}

		for entryName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}

			info := &advisorapi.ContainerAllocationInfo{
				Metadata: &advisorsvc.ContainerMetadata{
					PodUid:               allocationInfo.PodUid,
					PodNamespace:         allocationInfo.PodNamespace,
					PodName:              allocationInfo.PodName,
					ContainerName:        allocationInfo.ContainerName,
					ContainerIndex:       allocationInfo.ContainerIndex,
					Labels:               maputil.CopySS(allocationInfo.Labels),
					Annotations:          maputil.CopySS(allocationInfo.Annotations),
					QosLevel:             allocationInfo.QoSLevel,
					RequestQuantity:      uint64(allocationInfo.RequestQuantity),
					RequestMilliQuantity: uint64(allocationInfo.RequestQuantity * 1000),
					UseMilliQuantity:     true,
				},
				AllocationInfo: &advisorapi.AllocationInfo{
					RampUp:        allocationInfo.RampUp,
					OwnerPoolName: allocationInfo.OwnerPoolName,
				},
			}

			if !containerEntries.IsPoolEntry() {
				// Only fill in the container type for non-pool entries.
				containerType, found := pluginapi.ContainerType_value[allocationInfo.ContainerType]
				if !found {
					return nil, fmt.Errorf("container type %q for container %s/%s not found", allocationInfo.ContainerType, uid, entryName)
				}
				info.Metadata.ContainerType = pluginapi.ContainerType(containerType)

				ownerPoolName := allocationInfo.GetOwnerPoolName()
				if ownerPoolName == commonstate.EmptyOwnerPoolName {
					general.Warningf("pod: %s/%s container: %s get empty owner pool name",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
					if allocationInfo.CheckSideCar() {
						ownerPoolName = containerEntries.GetMainContainerPoolName()

						general.Warningf("set pod: %s/%s sidecar container: %s owner pool name: %s same to its main container",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName,
							ownerPoolName)
					}
				}
				info.AllocationInfo.OwnerPoolName = ownerPoolName

				// Copy annotations missing in sidecar container from main container.
				// This should have been done during sidecar allocation, but we do it again here
				// to ensure backward compatibility with checkpoint written by older versions of the plugin.
				if allocationInfo.CheckSideCar() {
					mainContainerInfo := containerEntries.GetMainContainerEntry()
					if mainContainerInfo != nil && len(mainContainerInfo.Annotations) > 0 {
						if info.Metadata.Annotations == nil {
							info.Metadata.Annotations = make(map[string]string, len(mainContainerInfo.Annotations))
						}
						for key, value := range mainContainerInfo.Annotations {
							if _, ok := info.Metadata.Annotations[key]; !ok {
								info.Metadata.Annotations[key] = value
							}
						}
					}
				}
			}

			// not set topology-aware assignments for shared_cores,
			// since their topology-aware assignments are same to the pools they are in.
			if !allocationInfo.CheckShared() || containerEntries.IsPoolEntry() {
				info.AllocationInfo.TopologyAwareAssignments = machine.ParseCPUAssignmentFormat(allocationInfo.TopologyAwareAssignments)
				info.AllocationInfo.OriginalTopologyAwareAssignments = machine.ParseCPUAssignmentFormat(allocationInfo.OriginalTopologyAwareAssignments)
			}

			chkEntries[uid].Entries[entryName] = info
		}
	}

	if lo.IsNil(p.advisor.featureGateManager) {
		return nil, fmt.Errorf("featureGateManager is nil")
	}
	wantedFeatureGates, err := p.advisor.featureGateManager.GetWantedFeatureGates(finders.FeatureGateTypeCPU)
	if err != nil {
		return nil, err
	}

	general.InfofV(6, "CPU plugin desire negotiation feature gates: %#v", wantedFeatureGates)

	numaResourcePackageStates := machineState.GetNUMAResourcePackageStates()
	var resourcePackageConfig *advisorapi.ResourcePackageConfig
	if len(numaResourcePackageStates) > 0 {
		resourcePackageConfig = &advisorapi.ResourcePackageConfig{
			NumaResourcePackages: make(map[uint64]*advisorapi.NumaResourcePackageConfig),
		}
		for numaID, pkgStates := range numaResourcePackageStates {
			numaConfig := &advisorapi.NumaResourcePackageConfig{
				Packages: make(map[string]*advisorapi.ResourcePackageItemConfig),
			}
			for pkgName, state := range pkgStates {
				numaConfig.Packages[pkgName] = &advisorapi.ResourcePackageItemConfig{
					PinnedCpuset: state.PinnedCPUSet.String(),
					Attributes:   state.Attributes,
				}
			}
			resourcePackageConfig.NumaResourcePackages[uint64(numaID)] = numaConfig
		}
	}

	return &advisorapi.GetAdviceRequest{
		Entries:               chkEntries,
		WantedFeatureGates:    wantedFeatureGates,
		ResourcePackageConfig: resourcePackageConfig,
	}, nil
}

func (p *DynamicPolicy) getAdviceFromAdvisor(ctx context.Context) (isImplemented bool, err error) {
	startTime := time.Now()
	general.Infof("called")
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	request, requestRevision, err := p.createGetAdviceRequestAtRevision()
	if err != nil {
		return false, fmt.Errorf("create GetAdviceRequest failed with error: %w", err)
	}
	resp, err := p.advisor.advisorClient.GetAdvice(ctx, request)
	if err != nil {
		if general.IsUnimplementedError(err) {
			return false, nil
		}
		return true, fmt.Errorf("GetAdvice failed with error: %w", err)
	}

	general.InfofV(6, "QRM CPU plugin wanted feature gates: %v, sysadvisor supported feature gates: %v", lo.Keys(request.WantedFeatureGates), lo.Keys(resp.SupportedFeatureGates))
	// check if there are feature gates wanted by QRM that are not supported by cpu sysadvisor
	wantedButNotSupportedFeatureGates := featuregatenegotiation.GetWantedButNotSupportedFeatureGates(request.WantedFeatureGates, resp.SupportedFeatureGates)
	for _, featureGate := range wantedButNotSupportedFeatureGates {
		if featureGate.MustMutuallySupported {
			return true, fmt.Errorf("feature gate %s which must be mutually supported is not supported by cpu-advisor", featureGate.Name)
		}
	}

	// Convert the advisor response into internal entries before taking the policy
	// lock. Target-driven reclaim is gated later by runtime config, not by
	// any wire-level negotiation.
	convertedResp := convertGetAdviceResponse(resp)

	err = p.allocateByCPUAdvisorAtRevision(
		request, convertedResp, resp.SupportedFeatureGates, requestRevision)
	if err != nil {
		return true, fmt.Errorf("allocate by GetAdvice response failed with error: %w", err)
	}

	if len(wantedButNotSupportedFeatureGates) > 0 {
		general.Warningf("feature gates wanted by QRM that are not supported by cpu sysadvisor: %v", lo.Keys(wantedButNotSupportedFeatureGates))
		return true, featuregatenegotiation.FeatureGatesNotSupportedError{WantedButNotSupportedFeatureGates: wantedButNotSupportedFeatureGates}
	}

	return true, nil
}

func convertGetAdviceResponse(resp *advisorapi.GetAdviceResponse) *advisorapi.ListAndWatchResponse {
	if resp == nil {
		return nil
	}

	return &advisorapi.ListAndWatchResponse{
		Entries:                               resp.Entries,
		AllowSharedCoresOverlapReclaimedCores: resp.AllowSharedCoresOverlapReclaimedCores,
		ExtraEntries:                          resp.ExtraEntries,
		DisableDedicatedCoresOverlapReclaimedCores: resp.DisableDedicatedCoresOverlapReclaimedCores,
	}
}

func validateDedicatedReclaimDisjointTransport(
	req *advisorapi.GetAdviceRequest,
	resp *advisorapi.ListAndWatchResponse,
	featureGates map[string]*advisorsvc.FeatureGate,
) error {
	if resp == nil || !resp.DisableDedicatedCoresOverlapReclaimedCores {
		return nil
	}
	if req == nil {
		return fmt.Errorf("dedicated reclaim disjoint partition requires negotiated GetAdvice")
	}
	if featureGates[feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition] == nil {
		return fmt.Errorf("feature gate %s is required for dedicated reclaim disjoint partition",
			feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition)
	}
	return nil
}

// getAdviceFromAdvisorLoop gets advice from cpu-advisor periodically.
// qrm-plugin works in client-mode, while cpu-advisor works in server-mode.
// All information required by cpu-advisor is included in the request, and the response is returned synchronously.
// This new communication mode is meant to replace the legacy list-and-watch mode.
// This function only returns if stopCh is closed, or if the advisor does not implement GetAdvice,
// in which case we should fall back to the legacy list-and-watch mode.
func (p *DynamicPolicy) getAdviceFromAdvisorLoop(stopCh <-chan struct{}) {
	general.Infof("called")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		general.Infof("received stop signal, stop calling GetAdvice on CPUAdvisorServer")
		cancel()
	}()

	_ = wait.PollImmediateUntilWithContext(ctx, p.config.getAdviceInterval, func(ctx context.Context) (bool, error) {
		isImplemented, err := p.getAdviceFromAdvisor(ctx)
		if err != nil {
			if featuregatenegotiation.IsFeatureGatesNotSupportedError(err) {
				_ = p.emitter.emitter.StoreInt64(util.MetricNameGetAdviceFeatureNotSupported, 1,
					metrics.MetricTypeNameRaw,
					metrics.MetricTag{
						Key: "error_message", Val: metric.MetricTagValueFormat(err),
					})
				general.Warningf("get advice from cpu advisor found not supported feature gate: %v", err)
			} else {
				_ = p.emitter.emitter.StoreInt64(util.MetricNameGetAdviceFailed, 1, metrics.MetricTypeNameRaw)
				general.Errorf("get advice from cpu advisor failed with error: %v", err)
			}
		} else if !isImplemented {
			general.Infof("CPUAdvisorServer does not implement GetAdvice")
			return true, nil
		}

		_ = general.UpdateHealthzStateByError(cpuconsts.CommunicateWithAdvisor, err)

		if p.advisor.advisorMonitor != nil && err == nil {
			p.advisor.advisorMonitor.UpdateRefreshTime()
		}
		return false, nil
	})
}

// lwCPUAdvisorServer works as a client to connect with cpu-advisor.
// it will wait to receive allocations from cpu-advisor, and perform allocate actions
func (p *DynamicPolicy) lwCPUAdvisorServer(stopCh <-chan struct{}) error {
	general.Infof("called")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-stopCh
		general.Infof("received stop signal, stop calling ListAndWatch of CPUAdvisorServer")
		cancel()
	}()

	stream, err := p.advisor.advisorClient.ListAndWatch(ctx, &advisorsvc.Empty{})
	if err != nil {
		return fmt.Errorf("call ListAndWatch of CPUAdvisorServer failed with error: %v", err)
	}

	for {
		resp, err := stream.Recv()
		if err != nil {
			_ = p.emitter.emitter.StoreInt64(util.MetricNameLWAdvisorServerFailed, 1, metrics.MetricTypeNameRaw)
			return fmt.Errorf("receive ListAndWatch response of CPUAdvisorServer failed with error: %v, grpc code: %v",
				err, status.Code(err))
		}

		// old asynchronous communication interface does not support feature gate negotiation. If necessary, upgrade to the synchronization interface.
		emptyMap := map[string]*advisorsvc.FeatureGate{}
		err = p.allocateByCPUAdvisor(nil, resp, emptyMap)
		if err != nil {
			general.Errorf("allocate by ListAndWatch response of CPUAdvisorServer failed with error: %v", err)
		}

		_ = general.UpdateHealthzStateByError(cpuconsts.CommunicateWithAdvisor, err)

		if p.advisor.advisorMonitor != nil && err == nil {
			p.advisor.advisorMonitor.UpdateRefreshTime()
		}
	}
}

// advisorRequestHasActiveRampUp reports whether the advisor request carries any
// active ramp-up. This is a node-global observation used for the stale check
// on both the hard-partition-enabled and disabled paths: the advisor request
// carries no allocation annotations, so per-domain ramp-up resolution is not
// possible on the request side. Domain-scoped hard-partition enforcement is
// derived from currentEntries (authoritative snapshot) and re-derived from
// newEntries on the response side.
func advisorRequestHasActiveRampUp(req *advisorapi.GetAdviceRequest) bool {
	if req == nil {
		return false
	}
	for _, containerEntries := range req.Entries {
		if containerEntries == nil {
			continue
		}
		if len(containerEntries.Entries) == 1 &&
			containerEntries.Entries[commonstate.FakedContainerName] != nil {
			continue
		}
		for _, containerInfo := range containerEntries.Entries {
			if containerInfo != nil && containerInfo.AllocationInfo != nil &&
				containerInfo.AllocationInfo.RampUp {
				return true
			}
		}
	}
	return false
}

// allocateByCPUAdvisor perform allocate actions based on allocation response from cpu-advisor.
// supportedFeatureGates means the feature gates than both qrm wanted and sysadvisor supported
func (p *DynamicPolicy) allocateByCPUAdvisor(
	req *advisorapi.GetAdviceRequest,
	resp *advisorapi.ListAndWatchResponse,
	featureGates map[string]*advisorsvc.FeatureGate,
) (err error) {
	return p.allocateByCPUAdvisorWithRevision(req, resp, featureGates, nil)
}

func (p *DynamicPolicy) allocateByCPUAdvisorAtRevision(
	req *advisorapi.GetAdviceRequest,
	resp *advisorapi.ListAndWatchResponse,
	featureGates map[string]*advisorsvc.FeatureGate,
	requestRevision uint64,
) error {
	return p.allocateByCPUAdvisorWithRevision(req, resp, featureGates, &requestRevision)
}

// allocateByCPUAdvisorWithRevision owns one advisor response transaction from
// request-generation validation through canonical state commit and post-commit
// apply. The request revision rejects stale RPC responses, while the later
// precommit revision CAS closes races introduced during local planning.
func (p *DynamicPolicy) allocateByCPUAdvisorWithRevision(
	req *advisorapi.GetAdviceRequest,
	resp *advisorapi.ListAndWatchResponse,
	featureGates map[string]*advisorsvc.FeatureGate,
	requestRevision *uint64,
) (err error) {
	if resp == nil {
		return fmt.Errorf("allocateByCPUAdvisor got nil qos aware lw response")
	}
	if err := validateDedicatedReclaimDisjointTransport(req, resp, featureGates); err != nil {
		return err
	}

	startTime := time.Now()
	general.Infof("allocateByCPUAdvisor is called")
	_ = p.emitter.emitter.StoreInt64(util.MetricNameHandleAdvisorRespCalled, 1, metrics.MetricTypeNameRaw)
	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.emitter.StoreInt64(util.MetricNameHandleAdvisorRespFailed, 1, metrics.MetricTypeNameRaw)
		}
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	leaseCtx, cancelLease := context.WithTimeout(
		context.Background(), cpuSetAdjustmentHandlerTimeout(p.config.conf))
	executionLease, leaseErr := p.acquireCPUSetAdjustmentExecutionLocked(leaseCtx)
	cancelLease()
	if leaseErr != nil {
		return leaseErr
	}
	defer executionLease.release()
	executionCtx := context.WithValue(
		context.Background(), cpuSetAdjustmentExecutionLeaseContextKey{}, executionLease)

	// A committed advisor revision is not complete until its post-commit
	// production apply succeeds. Reconcile that exact revision before planning
	// another frame so a failed staged migration cannot be overwritten by a
	// later frame while the source state has already advanced.
	if target := p.currentAdvisorPostCommitTarget(); target != nil {
		ctx, cancel := context.WithTimeout(executionCtx, cpuSetAdjustmentHandlerTimeout(p.config.conf))
		reconcileErr := p.reconcileOrRetireAdvisorPostCommitTarget(
			ctx, target, cpusetutil.CPUSetAdjustmentModeRetry)
		cancel()
		if reconcileErr != nil {
			return fmt.Errorf(
				"previous advisor post-commit apply for revision %d is still pending: %w",
				target.revision, reconcileErr)
		}
	}

	// The synchronous response is valid only for the exact state generation
	// used to build its request. The later precommit CAS protects mutations
	// during planning; this guard protects the RPC round-trip itself.
	currentRevision := p.state.GetRevision()
	if requestRevision != nil && currentRevision != *requestRevision {
		return fmt.Errorf("advisor request state revision is stale: expected=%d actual=%d",
			*requestRevision, currentRevision)
	}

	currentEntries := p.state.GetPodEntries()
	dynamicConf := p.config.dynamicConfig.GetDynamicConfiguration()

	// Staleness is checked with the node-global ramp-up observation on both
	// paths. The advisorapi request side carries no pod annotations, so a
	// NUMA-binding reclaim domain cannot be resolved from the request alone;
	// enforcing per-domain consistency from the request would be semantically
	// asymmetric (a non-binding shared ramp-up maps to the global domain on the
	// state side but to a single-NUMA placement on the request side). The
	// domain-scoped enforcement is instead derived solely from the committed
	// currentEntries, and the per-domain agreement is re-derived on the
	// response-side newEntries before it is materialized.
	// The hard-partition reclaim gate is owned by the registered
	// ReclaimStrategyHardPartitionRampUp strategy; this lookup is the live
	// control-flow use of the reclaim strategy registry.
	hardPartitionEnabled := hardPartitionReclaimConfigGated(dynamicConf)
	currentRampUpActive := currentEntries.HasActiveRampUp()
	if req != nil {
		requestRampUpActive := advisorRequestHasActiveRampUp(req)
		if requestRampUpActive != currentRampUpActive {
			return fmt.Errorf("advisor request ramp-up state is stale")
		}
		if vErr := p.advisor.advisorValidator.ValidateRequest(req); vErr != nil {
			return fmt.Errorf("ValidateCPUAdvisorReq failed with error: %v", vErr)
		}
	}
	hardActiveDomains := sets.NewInt()
	if hardPartitionEnabled && currentRampUpActive {
		resolvedDomains, dErr := currentEntries.ActiveRampUpDomains(p.machine.machineInfo.CPUTopology)
		if dErr != nil {
			return fmt.Errorf("resolve current active ramp-up domains for advisor: %w", dErr)
		}
		hardActiveDomains = resolvedDomains
	}
	vErr := p.validateAdvisorResponseWithDynamicConfig(resp, dynamicConf)
	if vErr != nil {
		return fmt.Errorf("ValidateCPUAdvisorResp failed with error: %v", vErr)
	}

	blockToCPUSet, checkpointTransition, aErr := p.generateBlockCPUSetWithCheckpointTransitionForAttempt(
		resp, featureGates, hardActiveDomains, dynamicConf)
	if aErr != nil {
		return fmt.Errorf("generateBlockCPUSet failed with error: %w", aErr)
	}
	if err := p.validateHardPartitionBlockPlanWithDynamicConfig(
		resp, blockToCPUSet, hardActiveDomains, dynamicConf); err != nil {
		return fmt.Errorf("validate hard-partition reclaim block plan failed: %w", err)
	}

	curAllowSharedCoresOverlapReclaimedCores := p.state.GetAllowSharedCoresOverlapReclaimedCores()
	if curAllowSharedCoresOverlapReclaimedCores != resp.AllowSharedCoresOverlapReclaimedCores {
		general.Infof("set allowSharedCoresOverlapReclaimedCores from %v to %v",
			curAllowSharedCoresOverlapReclaimedCores, resp.AllowSharedCoresOverlapReclaimedCores)
	}

	responseAllowOverlap := resp.AllowSharedCoresOverlapReclaimedCores
	hardActive := rampUpDomainsToLegacyHardActive(hardActiveDomains)
	pending, applyErr := p.applyBlocksWithDynamicConfig(
		blockToCPUSet, resp, responseAllowOverlap, hardActive, dynamicConf)
	if applyErr != nil {
		return fmt.Errorf("prepare applyBlocks failed with error: %w", applyErr)
	}
	pending.enforceSteadyReclaim = !hardActive &&
		advisorResponseHasFakeNUMAMandatoryReclaim(resp)
	pending.migrationCheckpointTransition = checkpointTransition
	if p.pendingAdvisorStateMatchesCommitted(pending) &&
		len(resp.ExtraEntries) == 0 &&
		!p.hasAnyPendingAdvisorPostCommitTarget() {
		if err := p.validateAdvisorPartitionBeforeCommitWithDynamicConfig(
			pending.entries,
			p.state.GetMachineState(),
			pending.allowOverlap,
			pending.disableDedicated,
			pending.dynamicConfig,
		); err != nil {
			return fmt.Errorf("validate converged advisor state failed with error: %w", err)
		}
		if err := p.steadyFakeNUMAMigration.applyTransition(
			pending.migrationCheckpointTransition); err != nil {
			return fmt.Errorf("apply converged migration checkpoint transition: %w", err)
		}
		return nil
	}
	target, applyErr := p.commitAdvisorResponseWithWriteAheadTransition(
		resp,
		pending.migrationCheckpointTransition,
		pending.preCommitRevision,
		func(target *advisorPostCommitTarget) error {
			return p.commitPendingAdvisorState(pending, target)
		},
	)
	if applyErr != nil {
		return fmt.Errorf("applyBlocks failed with error: %w", applyErr)
	}

	ctx, cancel := context.WithTimeout(executionCtx, cpuSetAdjustmentHandlerTimeout(p.config.conf))
	defer cancel()
	if err := p.reconcileAdvisorPostCommitTarget(ctx, target); err != nil {
		return err
	}

	return nil
}

func (p *DynamicPolicy) validateAdvisorResponse(resp *advisorapi.ListAndWatchResponse) error {
	var dynamicConf *dynamicconfig.Configuration
	if p != nil && p.config.dynamicConfig != nil {
		dynamicConf = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	return p.validateAdvisorResponseWithDynamicConfig(resp, dynamicConf)
}

func (p *DynamicPolicy) validateAdvisorResponseWithDynamicConfig(
	resp *advisorapi.ListAndWatchResponse,
	dynamicConf *dynamicconfig.Configuration,
) error {
	if dynamicConf != nil && dynamicConf.FillDefaultSharePoolWithNonReclaimCPUs {
		return p.advisor.advisorValidator.ValidateWithDefaultShareUpperBound(resp)
	}
	return p.advisor.advisorValidator.Validate(resp)
}

func (p *DynamicPolicy) applyCgroupConfigs(resp *advisorapi.ListAndWatchResponse) error {
	if p.applyCgroupConfigsHook != nil {
		return p.applyCgroupConfigsHook(resp)
	}
	for _, calculationInfo := range resp.ExtraEntries {
		if !general.IsPathExists(common.GetAbsCgroupPath(common.DefaultSelectedSubsys, calculationInfo.CgroupPath)) {
			general.Infof("cgroup path not exist, skip applyCgroupConfigs: %s", common.GetAbsCgroupPath(common.DefaultSelectedSubsys, calculationInfo.CgroupPath))
			continue
		}

		cgConf, ok := calculationInfo.CalculationResult.Values[string(advisorapi.ControlKnobKeyCgroupConfig)]
		if !ok {
			continue
		}

		resources := &common.CgroupResources{}
		err := json.Unmarshal([]byte(cgConf), resources)
		if err != nil {
			return fmt.Errorf("unmarshal %s: %s failed with error: %v",
				advisorapi.ControlKnobKeyCgroupConfig, cgConf, err)
		}

		resources.SkipDevices = true
		resources.SkipFreezeOnSet = true

		err = p.checkAndApplyIfCgroupV1(calculationInfo, resources)
		if err != nil {
			_ = p.emitter.emitter.StoreInt64(util.MetricNameCheckApplyV1Error, 1, metrics.MetricTypeNameCount)
			return fmt.Errorf("checkAndApplyIfCgroupV1 failed with error: %v", err)
		}

		err = common.ApplyCgroupConfigs(calculationInfo.CgroupPath, resources)
		if err != nil {
			return fmt.Errorf("ApplyCgroupConfigs failed: %s, %v", calculationInfo.CgroupPath, err)
		}
	}

	return nil
}

func (p *DynamicPolicy) checkAndApplyIfCgroupV1(calculationInfo *advisorsvc.CalculationInfo, resources *common.CgroupResources) error {
	ops := p.gops()
	if ops.checkCgroup2UnifiedMode() {
		return nil
	}

	currentParentCgroupCPUStats, err := ops.getCPUWithRelativePath(calculationInfo.CgroupPath)
	if err != nil {
		return fmt.Errorf("Get big group quota failed with error: %v", err)
	}

	// scale down the be group quota
	if currentParentCgroupCPUStats.CpuQuota < 0 || resources.CpuQuota <= currentParentCgroupCPUStats.CpuQuota {
		err := p.checkAndApplyAllPodsQuota(calculationInfo, resources.CpuQuota)
		if err != nil {
			return fmt.Errorf("checkAndApplyAllPodsQuota failed with error: %v", err)
		}
	} else {
		minBGQuota := currentParentCgroupCPUStats.CpuQuota
		if resources.CpuQuota < minBGQuota {
			minBGQuota = resources.CpuQuota
		}
		err := p.checkAndApplyAllPodsQuota(calculationInfo, minBGQuota)
		if err != nil {
			return fmt.Errorf("checkAndApplyAllPodsQuota failed with error: %v", err)
		}
	}
	return nil
}

func (p *DynamicPolicy) checkAndApplyAllPodsQuota(calculationInfo *advisorsvc.CalculationInfo, bigGroupQuota int64) error {
	ops := p.gops()
	if ops.checkAndApplyAllPodsQuota != nil {
		return ops.checkAndApplyAllPodsQuota(calculationInfo, bigGroupQuota)
	}
	podsPathMap, podDirs, err := p.getCurrentPathAllPodsDirAndMap(calculationInfo.CgroupPath)
	if err != nil {
		return err
	}

	for _, podDir := range podDirs {
		pod, podRelativePath, err := p.getPodAndRelativePath(calculationInfo.CgroupPath, podDir, podsPathMap)
		if err != nil {
			general.Warningf("getPodAndRelativePath error for pod dir %s: %v", podDir, err)
			continue
		}

		_, limit := resource.PodRequestsAndLimits(pod)
		if _, ok := limit[v1.ResourceCPU]; !ok {
			general.Warningf("no cpu limit for pod %s: %v", pod.Name, err)
			continue
		}

		podLimit := limit.Cpu().MilliValue() // Value() will lose precision of data
		podCpu, err := ops.getCPUWithRelativePath(podRelativePath)
		if err != nil {
			return fmt.Errorf("GetCPUWithRelativePath %s failed with error: %v", podRelativePath, err)
		}

		podRealQuota := podLimit * int64(podCpu.CpuPeriod) / 1000
		podCurrentQuota := podCpu.CpuQuota

		if podRealQuota <= bigGroupQuota {
			if podRealQuota == podCurrentQuota {
				continue
			}

			err = p.applyAllContainersQuota(pod, true)
			if err != nil {
				general.Errorf("applyAllContainersQuota for pod %v failed with error: %v", pod.Name, err)
				continue
			}

			err := ops.applyCPUWithRelativePath(podRelativePath, &common.CPUData{CpuQuota: podRealQuota})
			if err != nil {
				return fmt.Errorf("ApplyCPUWithRelativePath %s to realQuota %v  failed with error: %v", podRelativePath, podRealQuota, err)
			}
		} else {
			err = p.applyAllContainersQuota(pod, false)
			if err != nil {
				general.Errorf("applyAllContainersQuota for pod %v failed with error: %v", pod.Name, err)
				continue
			}
			err := ops.applyCPUWithRelativePath(podRelativePath, &common.CPUData{CpuQuota: -1})
			if err != nil {
				return fmt.Errorf("ApplyCPUWithRelativePath %s to -1 failed with error: %v", podRelativePath, err)
			}
		}
	}
	return nil
}

func (p *DynamicPolicy) getAllDirs(parentPath string) ([]string, error) {
	entries, err := p.gops().readDir(parentPath)
	if err != nil {
		return nil, err
	}

	dirs := make([]string, 0)

	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}

	return dirs, nil
}

func (p *DynamicPolicy) getAllPodsPathMap() (map[string]*v1.Pod, error) {
	pods, err := p.meta.metaServer.GetPodList(context.Background(), native.PodIsActive)
	if err != nil {
		return nil, fmt.Errorf("GetPodList failed with error: %v", err)
	}

	podAbsPathMap := make(map[string]*v1.Pod)

	for _, pod := range pods {
		if pod == nil {
			continue
		}
		podAbsPath, err := p.gops().getPodAbsCgroupPath(common.DefaultSelectedSubsys, string(pod.UID))
		if err != nil {
			general.Errorf("get pod %s absolute path failed with error: %v", pod.Name, err)
			continue
		}
		podAbsPathMap[podAbsPath] = pod
	}

	return podAbsPathMap, nil
}

func (p *DynamicPolicy) getPodAndRelativePath(currentCgroupPath string, podDir string, podsPathMap map[string]*v1.Pod) (*v1.Pod, string, error) {
	if h := p.gops().getPodAndRelativePath; h != nil {
		return h(currentCgroupPath, podDir, podsPathMap)
	}
	podRelativePath := filepath.Join(currentCgroupPath, podDir)
	podAbsPath := p.gops().getAbsCgroupPath(common.DefaultSelectedSubsys, podRelativePath)
	pod, ok := podsPathMap[podAbsPath]
	if !ok || pod == nil {
		return nil, "", fmt.Errorf("can not get pod with abs path: %s", podAbsPath)
	}

	if len(pod.Spec.Containers) == 0 {
		return nil, "", fmt.Errorf("pod %s has no containers", pod.Name)
	}

	return pod, podRelativePath, nil
}

func (p *DynamicPolicy) getCurrentPathAllPodsDirAndMap(currentCgroupPath string) (map[string]*v1.Pod, []string, error) {
	if h := p.gops().getCurrentPathAllPodsDirAndMap; h != nil {
		return h(currentCgroupPath)
	}
	podsPathMap, err := p.getAllPodsPathMap()
	if err != nil {
		return nil, nil, fmt.Errorf("getAllPodsPathMap failed with error: %v", err)
	}
	absPath := p.gops().getAbsCgroupPath(common.DefaultSelectedSubsys, currentCgroupPath)
	podDirs, err := p.getAllDirs(absPath)
	if err != nil {
		return nil, nil, fmt.Errorf("getAllPodsPath failed with error: %v", err)
	}

	return podsPathMap, podDirs, nil
}

func (p *DynamicPolicy) getAllContainersRelativePathMap(pod *v1.Pod) map[string]*v1.Container {
	if h := p.gops().getAllContainersRelativePathMap; h != nil {
		return h(pod)
	}
	ops := p.gops()
	containerPathMap := make(map[string]*v1.Container)

	for _, container := range pod.Spec.Containers {
		// Create a copy of the container to avoid the loop variable capture issue
		containerCopy := container

		containerID, err := ops.getContainerID(pod, container.Name)
		if err != nil {
			general.Errorf("get container %s container id failed with error: %v", container.Name, err)
			continue
		}
		containerRelativeCgroupPath, err := ops.getContainerRelativeCgroupPath(string(pod.UID), containerID)
		if err != nil {
			general.Errorf("get container %s relative cgroup path failed with error: %v", container.Name, err)
			continue
		}

		containerPathMap[containerRelativeCgroupPath] = &containerCopy
	}

	return containerPathMap
}

func (p *DynamicPolicy) applyAllContainersQuota(pod *v1.Pod, setToLimit bool) error {
	if h := p.gops().applyAllContainersQuota; h != nil {
		return h(pod, setToLimit)
	}
	ops := p.gops()
	allContainersRelativePathMap := p.getAllContainersRelativePathMap(pod)

	for relativePath, container := range allContainersRelativePathMap {
		limit := container.Resources.Limits.Cpu().MilliValue() // Value() will lose precision of data
		containerCpu, err := ops.getCPUWithRelativePath(relativePath)
		if err != nil {
			return fmt.Errorf("GetCPUWithRelativePath %s failed with error: %v", relativePath, err)
		}
		realQuota := limit * int64(containerCpu.CpuPeriod) / 1000
		if setToLimit {
			if realQuota == containerCpu.CpuQuota {
				continue
			}
			err := ops.applyCPUWithRelativePath(relativePath, &common.CPUData{CpuQuota: realQuota})
			if err != nil {
				return fmt.Errorf("ApplyCPUWithRelativePath %s to %v failed with error: %v", relativePath, realQuota, err)
			}
		} else {
			err := p.applyAllSubCgroupQuotaToUnLimit(relativePath)
			if err != nil {
				return fmt.Errorf("applyAllSubCgroupQuotaToUnLimit %s failed with error: %v", relativePath, err)
			}
			err = ops.applyCPUWithRelativePath(relativePath, &common.CPUData{CpuQuota: -1})
			if err != nil {
				return fmt.Errorf("ApplyCPUWithRelativePath %s to -1 failed with error: %v", relativePath, err)
			}
		}
	}

	return nil
}

func (p *DynamicPolicy) checkAndApplySubCgroupPath(path string, d os.DirEntry, err error) error {
	if err != nil {
		return err
	}
	if !d.IsDir() {
		return nil
	}

	ops := p.gops()
	subCPU, err := ops.getCPUWithAbsolutePath(path)
	if err != nil {
		return fmt.Errorf("GetCPUWithRelativePath %s failed with error: %v", path, err)
	}

	if subCPU.CpuQuota < 0 {
		return nil
	}

	err = ops.applyCPUWithAbsolutePath(path, &common.CPUData{CpuQuota: -1})
	if err != nil {
		general.Errorf("ApplyCPUWithAbsolutePath %s to -1 failed with error: %v", path, err)
		return fmt.Errorf("ApplyCPUWithAbsolutePath %s to -1 failed with error: %v", path, err)
	}

	return nil
}

func (p *DynamicPolicy) applyAllSubCgroupQuotaToUnLimit(containerRelativePath string) error {
	if h := p.gops().applyAllSubCgroupQuotaToUnLimit; h != nil {
		return h(containerRelativePath)
	}
	containerAbsPath := p.gops().getAbsCgroupPath(common.DefaultSelectedSubsys, containerRelativePath)

	return filepath.WalkDir(containerAbsPath, func(path string, d fs.DirEntry, err error) error {
		if path == containerAbsPath {
			return nil
		}
		return p.checkAndApplySubCgroupPath(path, d, err)
	})
}

// allocateStaticAndForbiddenPools allocates CPU sets for static and forbidden pools.
// It iterates through the configured static pools (e.g., reserve) and forbidden pools,
// assigning their pre-calculated or required CPU sets from the state, and deducting
// them from the globally available CPUs.
func (p *DynamicPolicy) allocateStaticAndForbiddenPools(resp *advisorapi.ListAndWatchResponse, blockCPUSet advisorapi.BlockCPUSet, availableCPUs machine.CPUSet) (machine.CPUSet, error) {
	for _, poolName := range state.StaticPools.List() {
		allocationInfo := p.state.GetAllocationInfo(poolName, commonstate.FakedContainerName)
		if allocationInfo == nil {
			continue
		}

		blocks, ok := resp.GeEntryNUMABlocks(poolName, commonstate.FakedContainerName, commonstate.FakedNUMAID)
		if !ok || len(blocks) != 1 {
			return availableCPUs, fmt.Errorf("blocks of pool: %s is invalid", poolName)
		}

		blockID := blocks[0].BlockId
		blockCPUSet[blockID] = allocationInfo.AllocationResult.Clone()
		availableCPUs = availableCPUs.Difference(blockCPUSet[blockID])
	}

	// Deduct CPUs that should not be allocated to user containers.
	notAllocatablePoolsCPUs := state.GetUnitedPoolsCPUs(p.state.GetPodEntries(), state.IsForbiddenPool, commonstate.IsSystemPool)
	availableCPUs = availableCPUs.Difference(notAllocatablePoolsCPUs)
	return availableCPUs, nil
}

func isDedicatedBlock(block *advisorapi.BlockInfo) bool {
	_, ok := block.OwnerPoolEntryMap[commonstate.PoolNameDedicated]
	return ok
}

func isSharedBlock(block *advisorapi.BlockInfo) bool {
	for poolName := range block.OwnerPoolEntryMap {
		if commonstate.IsIsolationPool(poolName) || commonstate.IsShareNUMABindingPool(poolName) {
			return true
		}
	}
	return false
}

type fakeNUMABlockClass int

const (
	fakeNUMABlockClassActualReclaim fakeNUMABlockClass = iota
	fakeNUMABlockClassNormalShare
	fakeNUMABlockClassIsolation
	fakeNUMABlockClassAmbiguous
)

func classifyFakeNUMABlock(block *advisorapi.BlockInfo) fakeNUMABlockClass {
	if block == nil || len(block.OwnerPoolEntryMap) == 0 {
		return fakeNUMABlockClassAmbiguous
	}

	var (
		hasReclaim   bool
		hasShare     bool
		hasIsolation bool
	)
	for poolName := range block.OwnerPoolEntryMap {
		switch {
		case poolName == commonstate.PoolNameReclaim:
			hasReclaim = true
		case commonstate.IsIsolationPool(poolName) || commonstate.IsShareNUMABindingPool(poolName):
			hasIsolation = true
		case poolName == commonstate.PoolNameDedicated:
			return fakeNUMABlockClassAmbiguous
		default:
			hasShare = true
		}
	}

	switch {
	case hasReclaim && !hasShare && !hasIsolation:
		return fakeNUMABlockClassActualReclaim
	case hasShare && !hasReclaim && !hasIsolation:
		return fakeNUMABlockClassNormalShare
	case hasIsolation && !hasReclaim:
		return fakeNUMABlockClassIsolation
	default:
		return fakeNUMABlockClassAmbiguous
	}
}

func getSingleNormalSharePoolName(block *advisorapi.BlockInfo) (string, bool) {
	if classifyFakeNUMABlock(block) != fakeNUMABlockClassNormalShare {
		return "", false
	}

	for poolName := range block.OwnerPoolEntryMap {
		return poolName, true
	}
	return "", false
}

func (p *DynamicPolicy) allocateFakeNUMANormalShareBlocks(
	blocks []*advisorapi.BlockInfo,
	blockCPUSet advisorapi.BlockCPUSet,
	availableCPUs *machine.CPUSet,
	nodeRemainingCPUs *machine.CPUSet,
	globalNonReclaimableCPUSet machine.CPUSet,
) error {
	for _, block := range blocks {
		if block == nil {
			continue
		}
		if _, found := blockCPUSet[block.BlockId]; found {
			continue
		}

		poolName, ok := getSingleNormalSharePoolName(block)
		if !ok {
			return fmt.Errorf("normal share block %s has no unique owner pool", block.BlockId)
		}
		quantity, err := general.CovertUInt64ToInt(block.Result)
		if err != nil {
			return fmt.Errorf("parse normal share block: %s result failed with error: %v", block.BlockId, err)
		}

		candidates := availableCPUs.Difference(globalNonReclaimableCPUSet)
		previous := machine.NewCPUSet()
		if allocationInfo := p.state.GetAllocationInfo(poolName, commonstate.FakedContainerName); allocationInfo != nil {
			previous = allocationInfo.AllocationResult.Clone()
		}

		cpuset, _, err := p.takeByTieredPreferredCPUs(candidates, []machine.CPUSet{previous}, quantity)
		if err != nil {
			return fmt.Errorf("allocate normal share block: %s pool: %s failed with error: %v",
				block.BlockId, poolName, err)
		}

		blockCPUSet[block.BlockId] = cpuset
		*availableCPUs = availableCPUs.Difference(cpuset)
		*nodeRemainingCPUs = nodeRemainingCPUs.Difference(cpuset)
		general.InfoS("allocated fake numa normal share block",
			"blockID", block.BlockId,
			"poolName", poolName,
			"previousCPUSet", previous.String(),
			"allocatedCPUSet", cpuset.String())
	}
	return nil
}

// allocateDedicatedBlocks allocates CPU sets for dedicated blocks.
// It iterates through the dedicated blocks and assigns them the CPUs they have already
// been allocated according to the state. It also subtracts these allocated CPUs
// from the available and remaining CPU sets.
func (p *DynamicPolicy) allocateDedicatedBlocks(
	numaID int,
	blocks []*advisorapi.BlockInfo,
	blockCPUSet advisorapi.BlockCPUSet,
	numaAvailableCPUs machine.CPUSet,
	nodeRemainingCPUs *machine.CPUSet,
	availableCPUs *machine.CPUSet,
	rpPinnedCPUSet map[string]machine.CPUSet,
	allPinnedCPUSets machine.CPUSet,
	withNUMABinding *bool,
) error {
	machineInfo := p.machine.machineInfo
	for _, block := range blocks {
		if block == nil {
			continue
		}

		entry := block.OwnerPoolEntryMap[commonstate.PoolNameDedicated]
		blockID := block.BlockId

		if _, found := blockCPUSet[blockID]; found {
			general.Warningf("block: %v already allocated", blockID)
			continue
		}

		blockResult, err := general.CovertUInt64ToInt(block.Result)
		if err != nil {
			return fmt.Errorf("parse block: %s result failed with error: %v", blockID, err)
		}

		allocationInfo := p.state.GetAllocationInfo(entry.EntryName, entry.SubEntryName)
		if allocationInfo == nil {
			continue
		}

		alreadyAllocatedCPUs, ok := allocationInfo.TopologyAwareAssignments[numaID]
		if !ok {
			continue
		}

		pinnedCPUSets := machine.NewCPUSet()
		pkg := allocationInfo.GetResourcePackageName()
		if pkg != "" && !rpPinnedCPUSet[pkg].IsEmpty() {
			pinnedCPUSets = rpPinnedCPUSet[pkg].Intersection(numaAvailableCPUs)
		}

		// Calculate current available CPUs on this NUMA node by intersecting the globally updated
		// availableCPUs with the static numaAvailableCPUs. This correctly computes the latest available CPUs
		// dynamically without needing a separate NUMA-level tracking variable.
		currentAvailableCPUs := availableCPUs.Intersection(numaAvailableCPUs)
		if !pinnedCPUSets.IsEmpty() {
			currentAvailableCPUs = currentAvailableCPUs.Intersection(pinnedCPUSets)
		} else {
			currentAvailableCPUs = currentAvailableCPUs.Difference(allPinnedCPUSets)
		}

		var cpuset machine.CPUSet
		// Get the CPUs that are both already allocated to this block and still available in the current context
		availableAlreadyAllocatedCPUs := alreadyAllocatedCPUs.Intersection(currentAvailableCPUs)

		if alreadyAllocatedCPUs.Size() == blockResult {
			// If the requested block size hasn't changed, we should ideally reuse the exact same allocation.
			// However, we must first verify that all previously allocated CPUs are still available.
			if availableAlreadyAllocatedCPUs.Size() != blockResult {
				return fmt.Errorf("NUMA Aware block: %s in NUMA: %d size not changed, but some CPUs are not available", blockID, numaID)
			}
			cpuset = alreadyAllocatedCPUs
		} else if availableAlreadyAllocatedCPUs.Size() >= blockResult {
			// If the block size decreased, we can fulfill the new size entirely from the previously allocated (and still available) CPUs.
			cpuset, err = calculator.TakeByTopology(machineInfo, availableAlreadyAllocatedCPUs, blockResult, true)
			if err != nil {
				return fmt.Errorf("allocate cpuset for NUMA Aware block: %s in NUMA: %d failed with error: %v", blockID, numaID, err)
			}
		} else {
			// If the block size increased, we keep whatever previously allocated CPUs are still available,
			// and allocate the remaining required CPUs from the pool of current available CPUs.
			cpuset, err = calculator.TakeByTopology(machineInfo, currentAvailableCPUs.Difference(availableAlreadyAllocatedCPUs), blockResult-availableAlreadyAllocatedCPUs.Size(), true)
			if err != nil {
				return fmt.Errorf("allocate cpuset for NUMA Aware block: %s in NUMA: %d failed with error: %v", blockID, numaID, err)
			}
			cpuset = cpuset.Union(availableAlreadyAllocatedCPUs)
		}

		blockCPUSet[blockID] = cpuset
		*nodeRemainingCPUs = nodeRemainingCPUs.Difference(cpuset)
		*availableCPUs = availableCPUs.Difference(cpuset)
		if withNUMABinding != nil {
			*withNUMABinding = true
		}
	}
	return nil
}

// allocateShareBlocks allocates CPU sets for share blocks.
// It iterates through the share blocks, allocating CPUs based on advisor results.
// For FakedNUMAID, it uses the global available CPUs. For specific NUMA nodes,
// it uses the NUMA-available CPUs. It subtracts the allocated CPUs from the remaining sets.
func (p *DynamicPolicy) allocateShareBlocks(
	numaID int,
	blocks []*advisorapi.BlockInfo,
	blockCPUSet advisorapi.BlockCPUSet,
	numaAvailableCPUs machine.CPUSet,
	nodeRemainingCPUs *machine.CPUSet,
	availableCPUs *machine.CPUSet,
	rpPinnedCPUSet map[string]machine.CPUSet,
	allPinnedCPUSets machine.CPUSet,
	withNUMABinding *bool,
	sourceBlockByPool map[string]string,
) error {
	machineInfo := p.machine.machineInfo
	sourceBlockResultByID, err := buildAdvisorSourceBlockResultByID(blocks, sourceBlockByPool)
	if err != nil {
		return err
	}
	for _, block := range blocks {
		if block == nil {
			continue
		}

		pinnedCPUSets := machine.NewCPUSet()
		for poolName := range block.OwnerPoolEntryMap {
			if commonstate.IsIsolationPool(poolName) || commonstate.IsShareNUMABindingPool(poolName) {
				_, pkg := resourcepackage.UnwrapOwnerPoolName(poolName)
				if pkg != "" && !rpPinnedCPUSet[pkg].IsEmpty() {
					pinnedCPUSets = rpPinnedCPUSet[pkg].Intersection(numaAvailableCPUs)
				}
				break
			}
		}

		blockID := block.BlockId
		if _, found := blockCPUSet[blockID]; found {
			continue
		}

		blockResult, err := general.CovertUInt64ToInt(block.Result)
		if err != nil {
			return fmt.Errorf("parse block: %s result failed with error: %v", blockID, err)
		}

		// Same as in allocateDedicatedBlocks, intersect the globally updated availableCPUs with
		// the static numaAvailableCPUs to get the latest available CPUs dynamically.
		currentAvailableCPUs := availableCPUs.Intersection(numaAvailableCPUs)
		if numaID == commonstate.FakedNUMAID {
			currentAvailableCPUs = *availableCPUs
		}

		if !pinnedCPUSets.IsEmpty() {
			currentAvailableCPUs = currentAvailableCPUs.Intersection(pinnedCPUSets)
		} else {
			currentAvailableCPUs = currentAvailableCPUs.Difference(allPinnedCPUSets)
		}

		carved, err := p.tryCarveAdvisorBlockFromSource(block, sourceBlockByPool, sourceBlockResultByID, blockCPUSet, currentAvailableCPUs, availableCPUs, nodeRemainingCPUs, numaID, blockResult)
		if err != nil {
			return err
		}
		if carved {
			if withNUMABinding != nil {
				*withNUMABinding = true
			}
			continue
		}

		var cpuset machine.CPUSet
		if numaID == commonstate.FakedNUMAID {
			cpuset, _, err = calculator.TakeByNUMABalance(machineInfo, currentAvailableCPUs, blockResult)
		} else {
			cpuset, err = calculator.TakeByTopology(machineInfo, currentAvailableCPUs, blockResult, false)
		}
		if err != nil {
			return fmt.Errorf("allocate cpuset for block: %s failed with error: %v", blockID, err)
		}

		blockCPUSet[blockID] = cpuset
		*nodeRemainingCPUs = nodeRemainingCPUs.Difference(cpuset)
		*availableCPUs = availableCPUs.Difference(cpuset)
		if withNUMABinding != nil {
			*withNUMABinding = true
		}
	}
	return nil
}

// generateReclaimBlockCPUSet generates BlockCPUSet for reclaim blocks using a separate phase.
// It iterates through the reclaim blocks and allocates CPUs from the available CPU pool.
// It explicitly excludes the unused non-reclaimable pinned CPUs (based on the provided
// disableReclaimSelector and resource packages) to ensure they are not used for reclaim workloads.
func (p *DynamicPolicy) generateReclaimBlockCPUSet(
	reclaimBlocksMap map[int][]*advisorapi.BlockInfo,
	nodeRemainingCPUs machine.CPUSet,
	availableCPUs machine.CPUSet,
	globalNonReclaimableCPUSet machine.CPUSet,
	blockCPUSet advisorapi.BlockCPUSet,
) error {
	machineInfo := p.machine.machineInfo
	topology := machineInfo.CPUTopology

	// Preferred reuse: read the previous reclaim pool cpuset so this recompute can
	// keep reclaim on its prior cores whenever they are still available, instead of
	// re-selecting from scratch. Re-selecting from scratch makes the advisor-side
	// reclaim jump to a near-disjoint region across recomputes, which is the
	// amplifier that drives the parent-shrink EBUSY. An empty previous set (first
	// allocation / pool just created) falls back to the legacy from-scratch take.
	prevReclaim, err := p.state.GetPodEntries().GetCPUSetForPool(commonstate.PoolNameReclaim)
	if err != nil {
		prevReclaim = machine.NewCPUSet()
	}

	// 1. Process NUMA-aware reclaim blocks
	for numaID, blocks := range reclaimBlocksMap {
		if numaID == commonstate.FakedNUMAID || len(blocks) == 0 {
			continue
		}
		numaAvailableCPUs := nodeRemainingCPUs.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))

		// Deduct the non-reclaimable CPUSet for this NUMA node
		currentAvailableCPUs := numaAvailableCPUs.Difference(globalNonReclaimableCPUSet)

		for _, block := range blocks {
			if block == nil {
				continue
			}
			blockID := block.BlockId
			if _, found := blockCPUSet[blockID]; found {
				continue
			}

			blockResult, err := general.CovertUInt64ToInt(block.Result)
			if err != nil {
				return fmt.Errorf("parse block: %s result failed with error: %v", blockID, err)
			}

			general.InfoS("generateReclaimBlockCPUSet allocating NUMA Aware block",
				"blockID", blockID,
				"numaID", numaID,
				"blockResult", blockResult,
				"numaAvailableCPUs", numaAvailableCPUs.String(),
				"globalNonReclaimableCPUSet", globalNonReclaimableCPUSet.String(),
				"currentAvailableCPUs", currentAvailableCPUs.String())

			// Prefer this NUMA node's slice of the previous reclaim cpuset so reclaim
			// stays in place across recomputes; only spill to fresh cores when the
			// prior cores were taken by dedicated/share or the requirement grew.
			// When there is no prior reclaim on this NUMA node, keep the legacy
			// topology-aware take so first-allocation core shape is unchanged.
			prevOnNUMA := prevReclaim.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
			var cpuset machine.CPUSet
			if prevOnNUMA.IsEmpty() {
				cpuset, err = calculator.TakeByTopology(machineInfo, currentAvailableCPUs, blockResult, false)
			} else {
				cpuset, _, err = p.takeByTieredPreferredCPUs(currentAvailableCPUs, []machine.CPUSet{prevOnNUMA}, blockResult)
			}
			if err != nil {
				return fmt.Errorf("allocate cpuset for NUMA Aware reclaim block: %s in NUMA: %d failed with error: %v", blockID, numaID, err)
			}

			blockCPUSet[blockID] = cpuset
			currentAvailableCPUs = currentAvailableCPUs.Difference(cpuset)
			availableCPUs = availableCPUs.Difference(cpuset)

			general.InfoS("generateReclaimBlockCPUSet allocated NUMA Aware block",
				"blockID", blockID,
				"numaID", numaID,
				"allocatedCPUSet", cpuset.String(),
				"currentAvailableCPUs", currentAvailableCPUs.String(),
				"availableCPUs", availableCPUs.String())
		}
	}

	// 2. Process non-NUMA-aware reclaim blocks
	if blocks, ok := reclaimBlocksMap[commonstate.FakedNUMAID]; ok && len(blocks) > 0 {
		// Deduct the global non-reclaimable CPUSet to ensure non-NUMA-aware
		// reclaim/share blocks do not overlap with non-reclaimable pinned CPUs.
		currentAvailableCPUs := availableCPUs.Difference(globalNonReclaimableCPUSet)

		for _, block := range blocks {
			if block == nil {
				continue
			}
			blockID := block.BlockId
			if _, found := blockCPUSet[blockID]; found {
				continue
			}

			blockResult, err := general.CovertUInt64ToInt(block.Result)
			if err != nil {
				return fmt.Errorf("parse block: %s result failed with error: %v", blockID, err)
			}

			general.InfoS("generateReclaimBlockCPUSet allocating non-NUMA Aware block",
				"blockID", blockID,
				"blockResult", blockResult,
				"availableCPUs", availableCPUs.String(),
				"globalNonReclaimableCPUSet", globalNonReclaimableCPUSet.String(),
				"currentAvailableCPUs", currentAvailableCPUs.String())

			// Prefer the whole previous reclaim cpuset as the first tier so non-NUMA
			// reclaim also stays in place across recomputes; spill to NUMA-balanced
			// fresh cores only when the prior cores are no longer available.
			cpuset, _, err := p.takeByTieredPreferredCPUs(currentAvailableCPUs, []machine.CPUSet{prevReclaim}, blockResult)
			if err != nil {
				return fmt.Errorf("allocate cpuset for non NUMA Aware reclaim block: %s failed with error: %v", blockID, err)
			}

			blockCPUSet[blockID] = cpuset
			currentAvailableCPUs = currentAvailableCPUs.Difference(cpuset)
			availableCPUs = availableCPUs.Difference(cpuset)

			general.InfoS("generateReclaimBlockCPUSet allocated non-NUMA Aware block",
				"blockID", blockID,
				"allocatedCPUSet", cpuset.String(),
				"currentAvailableCPUs", currentAvailableCPUs.String(),
				"availableCPUs", availableCPUs.String())
		}
	}

	return nil
}

// generateBlockCPUSet keeps legacy allocation completely isolated from the negotiated
// dedicated/reclaim disjoint planner.
// rampUpDomainsToLegacyHardActive converts the domain-scoped ramp-up set into the
// node-global boolean used by the legacy block-planning helpers. The per-NUMA
// scoping happens upstream; here we only need to know whether any ramp-up domain
// is active.
func rampUpDomainsToLegacyHardActive(hardActiveDomains sets.Int) bool {
	return hardActiveDomains.Len() > 0
}

func (p *DynamicPolicy) generateBlockCPUSet(
	resp *advisorapi.ListAndWatchResponse,
	featureGates map[string]*advisorsvc.FeatureGate,
	hardActive bool,
) (advisorapi.BlockCPUSet, error) {
	blockCPUSet, _, err := p.generateBlockCPUSetWithCheckpointTransition(
		resp, featureGates, hardActive)
	return blockCPUSet, err
}

func (p *DynamicPolicy) generateBlockCPUSetWithCheckpointTransition(
	resp *advisorapi.ListAndWatchResponse,
	featureGates map[string]*advisorsvc.FeatureGate,
	hardActive bool,
) (
	advisorapi.BlockCPUSet,
	steadyFakeNUMAMigrationCheckpointTransition,
	error,
) {
	domains := sets.NewInt()
	if hardActive {
		domains.Insert(commonstate.FakedNUMAID)
	}
	return p.generateBlockCPUSetWithCheckpointTransitionForAttempt(
		resp, featureGates, domains, p.config.dynamicConfig.GetDynamicConfiguration())
}

func (p *DynamicPolicy) generateBlockCPUSetWithCheckpointTransitionForAttempt(
	resp *advisorapi.ListAndWatchResponse,
	featureGates map[string]*advisorsvc.FeatureGate,
	hardActiveDomains sets.Int,
	dynamicConf *dynamicconfig.Configuration,
) (
	advisorapi.BlockCPUSet,
	steadyFakeNUMAMigrationCheckpointTransition,
	error,
) {
	hardActive := rampUpDomainsToLegacyHardActive(hardActiveDomains)
	keep := steadyFakeNUMAMigrationCheckpointTransition{
		kind: steadyFakeNUMAMigrationCheckpointKeep,
	}
	if resp == nil {
		return nil, keep, fmt.Errorf("got nil resp")
	}
	planningResp := resp
	if dynamicConf != nil &&
		dynamicConf.FillDefaultSharePoolWithNonReclaimCPUs {
		// Default share is a synthetic upper-bound block. Preserve it in the
		// original response for applyBlocks, but never allocate it as an exact
		// descriptor before QRM materializes the residual.
		planningResp = resp.WithoutDefaultShareEntry()
	}
	if !planningResp.DisableDedicatedCoresOverlapReclaimedCores {
		blockCPUSet, err := p.generateLegacyBlockCPUSetWithDynamicConfig(
			planningResp, hardActive, dynamicConf)
		return blockCPUSet, keep, err
	}

	if featureGates[feature_cpu.NegotiationFeatureGateDedicatedReclaimDisjointPartition] == nil {
		return nil, keep, fmt.Errorf(
			"dedicated reclaim disjoint partition capability is not negotiated")
	}
	transition := keep
	blockCPUSet, err := p.planDisjointAdvisorBlocksWithCheckpointTransitionAndDynamicConfig(
		planningResp, hardActive, &transition, dynamicConf)
	return blockCPUSet, transition, err
}

func (p *DynamicPolicy) validateHardPartitionBlockPlan(
	resp *advisorapi.ListAndWatchResponse,
	blockCPUSet advisorapi.BlockCPUSet,
	hardActive bool,
) error {
	var dynamicConf *dynamicconfig.Configuration
	if p != nil && p.config.conf != nil {
		dynamicConf = p.config.conf.GetDynamicConfiguration()
	}
	domains := sets.NewInt()
	if hardActive {
		domains.Insert(commonstate.FakedNUMAID)
	}
	return p.validateHardPartitionBlockPlanWithDynamicConfig(
		resp, blockCPUSet, domains, dynamicConf)
}

func (p *DynamicPolicy) validateHardPartitionBlockPlanWithDynamicConfig(
	resp *advisorapi.ListAndWatchResponse,
	blockCPUSet advisorapi.BlockCPUSet,
	hardActiveDomains sets.Int,
	dynamicConf *dynamicconfig.Configuration,
) error {
	hardActive := rampUpDomainsToLegacyHardActive(hardActiveDomains)
	if !hardActive {
		return nil
	}
	if resp == nil || p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return fmt.Errorf("hard-partition reclaim validation requires advisor response and CPU topology")
	}

	machineState := p.state.GetMachineState()
	rpPinnedCPUSet := machineState.GetResourcePackagePinnedCPUSet()
	if dynamicConf == nil {
		return fmt.Errorf("hard-partition reclaim validation requires dynamic configuration")
	}
	selectorText := dynamicConf.DisableReclaimPinnedCPUSetResourcePackageSelector
	disableReclaimSelector, err := general.ParseSelector(selectorText)
	if err != nil {
		return err
	}
	nonReclaimableCPUSet := cpuutil.GetAggResourcePackagePinnedCPUSet(disableReclaimSelector, machineState)
	buildDescriptors := buildAdvisorBlockDescriptors
	if !resp.DisableDedicatedCoresOverlapReclaimedCores {
		buildDescriptors = buildLegacyMandatoryReclaimDescriptors
	}
	descriptors, err := buildDescriptors(resp, p.machine.machineInfo.CPUDetails, p.state.GetPodEntries(),
		rpPinnedCPUSet, nonReclaimableCPUSet)
	if err != nil {
		return err
	}
	available, err := p.allocateStaticAndForbiddenPools(
		resp, advisorapi.NewBlockCPUSet(), p.machine.machineInfo.CPUDetails.CPUs())
	if err != nil {
		return err
	}
	// NUMAs already owned by a committed steady exclusive DNB keep only their
	// finalized reserve once ramp-up ends, regardless of reclaimability; the
	// node-level imbalance guard must not re-impose the ratio-derived target on
	// them, otherwise it rejects every other ramp-up QoS on the node.
	skipNUMAs := p.state.GetPodEntries().SteadyExclusiveNUMAs(p.machine.machineInfo.CPUTopology)
	mandatory := filterAdvisorDescriptors(descriptors, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.Class == advisorBlockClassMandatoryReclaim
	})
	fakeDescriptors := filterAdvisorDescriptors(mandatory, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.NUMAID == commonstate.FakedNUMAID
	})
	if len(fakeDescriptors) > 1 {
		return fmt.Errorf("hard-partition reclaim validation expected at most one fake-NUMA block, got %d",
			len(fakeDescriptors))
	}
	if len(fakeDescriptors) == 0 ||
		(len(fakeDescriptors) == 1 && fakeDescriptors[0].Quantity == 0) {
		if len(fakeDescriptors) == 1 &&
			!blockCPUSet[fakeDescriptors[0].BlockID].IsEmpty() {
			return fmt.Errorf("hard-partition zero-quantity fake reclaim block %q is not empty",
				fakeDescriptors[0].BlockID)
		}
		reclaim := machine.NewCPUSet()
		eligible := machine.NewCPUSet()
		for _, descriptor := range mandatory {
			if descriptor.NUMAID == commonstate.FakedNUMAID {
				continue
			}
			reclaim = reclaim.Union(blockCPUSet[descriptor.BlockID])
			eligible = eligible.Union(descriptor.Eligible.Intersection(available))
		}
		if len(fakeDescriptors) == 1 && eligible.IsEmpty() {
			return nil
		}
		cpusPerCore := p.machine.machineInfo.CPUTopology.CPUsPerCore()
		return validateHardPartitionReclaimDistributionWithEffectiveCapacity(
			reclaim, eligible, nil, p.machine.machineInfo.CPUTopology, skipNUMAs,
			cpusPerCore, minimumHardReclaimCoresPerNUMA*cpusPerCore)
	}

	if resp.DisableDedicatedCoresOverlapReclaimedCores {
		descriptors, err = normalizeAdvisorDescriptorsForHardPartitionWholeCoreReclaim(
			descriptors, available, p.machine.machineInfo.CPUTopology, skipNUMAs)
		if err != nil {
			return err
		}
	}
	mandatory = filterAdvisorDescriptors(descriptors, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.Class == advisorBlockClassMandatoryReclaim
	})
	fakeDescriptors = filterAdvisorDescriptors(mandatory, func(descriptor advisorBlockDescriptor) bool {
		return descriptor.NUMAID == commonstate.FakedNUMAID
	})

	realReclaim := machine.NewCPUSet()
	realEligible := machine.NewCPUSet()
	for _, descriptor := range mandatory {
		if descriptor.NUMAID == commonstate.FakedNUMAID {
			continue
		}
		numaCPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(descriptor.NUMAID)
		realReclaim = realReclaim.Union(blockCPUSet[descriptor.BlockID])
		realEligible = realEligible.Union(
			descriptor.Eligible.Intersection(available).Intersection(numaCPUs))
	}
	if outside := realReclaim.Difference(realEligible); !outside.IsEmpty() {
		return fmt.Errorf("hard-partition real reclaim outside eligible CPUs: %s", outside.String())
	}
	if err := assertCoreAligned(realReclaim, p.machine.machineInfo.CPUTopology); err != nil {
		return fmt.Errorf("hard-partition real reclaim is not core-aligned: %w", err)
	}

	fake := fakeDescriptors[0]
	cpusPerCore, err := mandatoryReclaimCoreWidth(
		descriptors, available, p.machine.machineInfo.CPUTopology, skipNUMAs)
	if err != nil {
		return fmt.Errorf("hard-partition reclaim validation: %w", err)
	}
	fakeCapacity := hardPartitionFakeNUMAReclaimCapacity(
		fake, descriptors, available, p.machine.machineInfo.CPUTopology, skipNUMAs)
	return validateHardPartitionReclaimDistributionWithEffectiveCapacity(
		blockCPUSet[fake.BlockID], fakeCapacity.rawEligible, fakeCapacity.effectiveByNUMA,
		p.machine.machineInfo.CPUTopology, skipNUMAs, cpusPerCore,
		minimumHardReclaimCoresPerNUMA*cpusPerCore)
}

func validateHardPartitionReclaimDistribution(
	reclaim machine.CPUSet,
	eligible machine.CPUSet,
	topology *machine.CPUTopology,
	excludedNUMAs sets.Int,
	minimumPerNUMA int,
) error {
	cpusPerCore := 0
	if topology != nil {
		cpusPerCore = topology.CPUsPerCore()
	}
	return validateHardPartitionReclaimDistributionWithEffectiveCapacity(
		reclaim, eligible, nil, topology, excludedNUMAs, cpusPerCore, minimumPerNUMA)
}

func validateHardPartitionReclaimDistributionWithEffectiveCapacity(
	reclaim machine.CPUSet,
	eligible machine.CPUSet,
	effectiveCapacityByNUMA map[int]int,
	topology *machine.CPUTopology,
	excludedNUMAs sets.Int,
	cpusPerCore int,
	minimumPerNUMA int,
) error {
	if topology == nil {
		return fmt.Errorf("hard-partition reclaim validation requires CPU topology")
	}
	if cpusPerCore <= 0 {
		return fmt.Errorf("hard-partition reclaim validation requires positive cpus per core, got %d", cpusPerCore)
	}
	machineCPUs := topology.CPUDetails.CPUs()
	if outside := reclaim.Difference(machineCPUs); !outside.IsEmpty() {
		return fmt.Errorf("hard-partition reclaim outside machine topology: %s", outside.String())
	}
	if outside := reclaim.Difference(eligible); !outside.IsEmpty() {
		return fmt.Errorf("hard-partition reclaim outside eligible CPUs: %s", outside.String())
	}
	if err := assertCoreAligned(reclaim, topology); err != nil {
		return fmt.Errorf("hard-partition reclaim is not core-aligned: %w", err)
	}

	countByNUMA := make(map[int]int)
	movableByNUMA := make(map[int]int)
	completeCapacityByNUMA := make(map[int]int)
	firstNonSkippedNUMA := -1
	participatingNUMAs := make([]int, 0, topology.NumNUMANodes)
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		if excludedNUMAs.Has(numaID) {
			continue
		}
		if firstNonSkippedNUMA < 0 {
			firstNonSkippedNUMA = numaID
		}
		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		completeCapacity := len(coreAlignedCandidates(
			topology, eligible.Intersection(numaCPUs), machine.NewCPUSet())) * cpusPerCore
		if effectiveCapacityByNUMA != nil {
			completeCapacity = general.Min(
				completeCapacity, effectiveCapacityByNUMA[numaID])
		}
		if completeCapacity == 0 {
			continue
		}
		count := reclaim.Intersection(numaCPUs).Size()
		effectiveCapacity := completeCapacity
		if effectiveCapacityByNUMA != nil {
			effectiveCapacity = effectiveCapacityByNUMA[numaID]
		}
		if count > effectiveCapacity {
			return fmt.Errorf(
				"hard-partition reclaim NUMA %d has %d CPUs, effective capacity is %d",
				numaID, count, effectiveCapacity)
		}
		movableByNUMA[numaID] = count
		if count < minimumPerNUMA {
			return fmt.Errorf("hard-partition reclaim NUMA %d has %d CPUs, minimum is %d",
				numaID, count, minimumPerNUMA)
		}
		countByNUMA[numaID] = count
		completeCapacityByNUMA[numaID] = completeCapacity
		participatingNUMAs = append(participatingNUMAs, numaID)
	}
	if len(countByNUMA) == 0 && firstNonSkippedNUMA >= 0 {
		return fmt.Errorf("hard-partition reclaim NUMA %d has 0 CPUs, minimum is %d",
			firstNonSkippedNUMA, minimumPerNUMA)
	}

	for _, lowNUMA := range participatingNUMAs {
		lowCount := countByNUMA[lowNUMA]
		if lowCount+cpusPerCore > completeCapacityByNUMA[lowNUMA] {
			continue
		}
		for _, highNUMA := range participatingNUMAs {
			highCount := countByNUMA[highNUMA]
			if highCount-lowCount > cpusPerCore &&
				movableByNUMA[highNUMA] >= cpusPerCore {
				return fmt.Errorf(
					"hard-partition reclaim is imbalanced across physical NUMAs: "+
						"NUMA %d can accept another core at count %d while another NUMA has %d",
					lowNUMA, lowCount, highCount)
			}
		}
	}
	return nil
}

// generateLegacyBlockCPUSetWithDynamicConfig computes the CPUSet allocation for all requested blocks using a two-phase allocation process.
// Phase 1 (High Priority): Allocates Dedicated and Share blocks, resolving NUMA boundaries and updating available CPUs.
// Phase 2 (Low Priority): Allocates Reclaim blocks, ensuring they don't use non-reclaimable pinned CPUs or CPUs already taken.
func (p *DynamicPolicy) generateLegacyBlockCPUSetWithDynamicConfig(
	resp *advisorapi.ListAndWatchResponse,
	hardActive bool,
	dynamicConf *dynamicconfig.Configuration,
) (advisorapi.BlockCPUSet, error) {
	if resp == nil {
		return nil, fmt.Errorf("got nil resp")
	}

	numaToBlocks, err := resp.GetBlocks()
	if err != nil {
		return nil, err
	}
	sourceBlockByPool := buildAdvisorSourceBlockByPool(numaToBlocks)

	machineInfo := p.machine.machineInfo
	topology := machineInfo.CPUTopology
	availableCPUs := topology.CPUDetails.CPUs()

	rpPinnedCPUSet := p.state.GetMachineState().GetResourcePackagePinnedCPUSet()
	allPinnedCPUSets := machine.NewCPUSet()
	for _, cset := range rpPinnedCPUSet {
		allPinnedCPUSets = allPinnedCPUSets.Union(cset)
	}

	blockCPUSet := advisorapi.NewBlockCPUSet()

	// 1. Allocate Static and Forbidden Pools
	availableCPUs, err = p.allocateStaticAndForbiddenPools(resp, blockCPUSet, availableCPUs)
	if err != nil {
		return nil, err
	}
	nodeRemainingCPUs := availableCPUs.Clone()

	// Get non-reclaimable pinned CPUSets
	if dynamicConf == nil {
		return nil, fmt.Errorf("legacy block planning requires dynamic configuration")
	}
	disableReclaimSelectorStr := dynamicConf.DisableReclaimPinnedCPUSetResourcePackageSelector
	disableReclaimSelector, err := general.ParseSelector(disableReclaimSelectorStr)
	if err != nil {
		return nil, err
	}
	machineState := p.state.GetMachineState()
	globalNonReclaimableCPUSet := cpuutil.GetAggResourcePackagePinnedCPUSet(disableReclaimSelector, machineState)

	general.InfoS("generateBlockCPUSet variables after allocateStaticAndForbiddenPools",
		"allPinnedCPUSets", allPinnedCPUSets.String(),
		"nodeRemainingCPUs", nodeRemainingCPUs.String(),
		"globalNonReclaimableCPUSet", globalNonReclaimableCPUSet.String(),
		"disableReclaimSelector", disableReclaimSelector.String())

	reclaimBlocksMap := make(map[int][]*advisorapi.BlockInfo)

	// Phase 1: Allocate Dedicated and Share blocks
	for numaID, blocks := range numaToBlocks {
		if numaID == commonstate.FakedNUMAID {
			continue
		}

		var dedicatedBlocks, shareBlocks, reclaimBlocks []*advisorapi.BlockInfo
		for _, block := range blocks {
			if isDedicatedBlock(block) {
				dedicatedBlocks = append(dedicatedBlocks, block)
			} else if isSharedBlock(block) {
				shareBlocks = append(shareBlocks, block)
			} else {
				reclaimBlocks = append(reclaimBlocks, block)
			}
		}
		reclaimBlocksMap[numaID] = reclaimBlocks

		numaAvailableCPUs := availableCPUs.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		withNUMABindingShareOrDedicatedPod := false

		err = p.allocateDedicatedBlocks(numaID, dedicatedBlocks, blockCPUSet, numaAvailableCPUs, &nodeRemainingCPUs, &availableCPUs, rpPinnedCPUSet, allPinnedCPUSets, &withNUMABindingShareOrDedicatedPod)
		if err != nil {
			return nil, err
		}

		general.InfoS("generateBlockCPUSet variables after allocateDedicatedBlocks",
			"numaID", numaID,
			"withNUMABindingShareOrDedicatedPod", withNUMABindingShareOrDedicatedPod,
			"numaAvailableCPUs", numaAvailableCPUs.String(),
			"nodeRemainingCPUs", nodeRemainingCPUs.String(),
			"availableCPUs", availableCPUs.String())

		err = p.allocateShareBlocks(numaID, shareBlocks, blockCPUSet, numaAvailableCPUs, &nodeRemainingCPUs, &availableCPUs, rpPinnedCPUSet, allPinnedCPUSets, &withNUMABindingShareOrDedicatedPod, sourceBlockByPool)
		if err != nil {
			return nil, err
		}

		if withNUMABindingShareOrDedicatedPod {
			// If there is any NUMA-binding share or dedicated pod on this NUMA node,
			// the entire NUMA node is excluded from the global pool (availableCPUs)
			// to avoid cross-NUMA interference from non-NUMA-aware workloads.
			availableCPUs = availableCPUs.Difference(numaAvailableCPUs)
		}

		general.InfoS("generateBlockCPUSet variables after allocateShareBlocks",
			"numaID", numaID,
			"withNUMABindingShareOrDedicatedPod", withNUMABindingShareOrDedicatedPod,
			"numaAvailableCPUs", numaAvailableCPUs.String(),
			"nodeRemainingCPUs", nodeRemainingCPUs.String(),
			"availableCPUs", availableCPUs.String())
	}

	if hardActive {
		reserved, err := p.preallocateLegacyHardReclaim(
			resp, nodeRemainingCPUs, rpPinnedCPUSet, globalNonReclaimableCPUSet, blockCPUSet)
		if err != nil {
			return nil, fmt.Errorf("preallocate legacy hard reclaim: %w", err)
		}
		nodeRemainingCPUs = nodeRemainingCPUs.Difference(reserved)
		availableCPUs = availableCPUs.Difference(reserved)
	}

	// Phase 2 for FakedNUMAID
	if blocks, ok := numaToBlocks[commonstate.FakedNUMAID]; ok {
		var normalShareBlocks, isolationBlocks, reclaimBlocks []*advisorapi.BlockInfo
		for _, block := range blocks {
			switch classifyFakeNUMABlock(block) {
			case fakeNUMABlockClassNormalShare:
				normalShareBlocks = append(normalShareBlocks, block)
			case fakeNUMABlockClassIsolation:
				isolationBlocks = append(isolationBlocks, block)
			case fakeNUMABlockClassActualReclaim:
				reclaimBlocks = append(reclaimBlocks, block)
			default:
				return nil, fmt.Errorf("ambiguous fake numa block: %v", block)
			}
		}
		reclaimBlocksMap[commonstate.FakedNUMAID] = reclaimBlocks

		err = p.allocateAdvisorSourceBlocksForCarve(normalShareBlocks, isolationBlocks, blockCPUSet, &availableCPUs, &nodeRemainingCPUs, globalNonReclaimableCPUSet, sourceBlockByPool)
		if err != nil {
			return nil, err
		}

		emptyNUMA := machine.NewCPUSet()
		err = p.allocateShareBlocks(commonstate.FakedNUMAID, isolationBlocks, blockCPUSet, emptyNUMA, &nodeRemainingCPUs, &availableCPUs, rpPinnedCPUSet, allPinnedCPUSets, nil, sourceBlockByPool)
		if err != nil {
			return nil, err
		}

		err = p.allocateFakeNUMANormalShareBlocks(normalShareBlocks, blockCPUSet, &availableCPUs, &nodeRemainingCPUs, globalNonReclaimableCPUSet)
		if err != nil {
			return nil, err
		}

		general.InfoS("generateBlockCPUSet variables after allocateShareBlocks for FakedNUMAID",
			"nodeRemainingCPUs", nodeRemainingCPUs.String(),
			"availableCPUs", availableCPUs.String())
	}

	// Phase 3: Allocate Reclaim blocks
	err = p.generateReclaimBlockCPUSet(reclaimBlocksMap, nodeRemainingCPUs, availableCPUs, globalNonReclaimableCPUSet, blockCPUSet)
	if err != nil {
		return nil, err
	}

	return blockCPUSet, nil
}

func (p *DynamicPolicy) preallocateLegacyHardReclaim(
	resp *advisorapi.ListAndWatchResponse,
	available machine.CPUSet,
	rpPinnedCPUSet map[string]machine.CPUSet,
	nonReclaimableCPUSet machine.CPUSet,
	blockCPUSet advisorapi.BlockCPUSet,
) (machine.CPUSet, error) {
	mandatory, err := buildLegacyMandatoryReclaimDescriptors(
		resp,
		p.machine.machineInfo.CPUDetails,
		p.state.GetPodEntries(),
		rpPinnedCPUSet,
		nonReclaimableCPUSet,
	)
	if err != nil {
		return machine.NewCPUSet(), err
	}
	// NUMAs owned by a committed steady exclusive DNB keep only their finalized
	// reserve once ramp-up ends; the planner must skip them so its per-NUMA
	// minimum and cross-NUMA imbalance guards do not re-impose the ratio-derived
	// target and reject every other ramp-up QoS on the node.
	skipNUMAs := p.state.GetPodEntries().SteadyExclusiveNUMAs(p.machine.machineInfo.CPUTopology)
	demands, blockIDByDemandKey, err := expandHardPartitionReclaimPhase(
		mandatory, available, p.machine.machineInfo.CPUTopology, skipNUMAs)
	if err != nil {
		return machine.NewCPUSet(), err
	}
	assignments, err := solveDisjointPartitions(demands, p.machine.machineInfo.CPUTopology)
	if err != nil {
		return machine.NewCPUSet(), err
	}

	reserved := machine.NewCPUSet()
	for demandKey, cpus := range assignments {
		blockID := blockIDByDemandKey[demandKey]
		blockCPUSet[blockID] = blockCPUSet[blockID].Union(cpus)
		reserved = reserved.Union(cpus)
	}
	return reserved, nil
}

func buildLegacyMandatoryReclaimDescriptors(
	resp *advisorapi.ListAndWatchResponse,
	cpuDetails machine.CPUDetails,
	podEntries state.PodEntries,
	rpPinnedCPUSet map[string]machine.CPUSet,
	nonReclaimableCPUSet machine.CPUSet,
) ([]advisorBlockDescriptor, error) {
	if resp == nil {
		return nil, fmt.Errorf("got nil advisor response")
	}

	mandatoryResp := &advisorapi.ListAndWatchResponse{
		Entries: make(map[string]*advisorapi.CalculationEntries),
	}
	for entryName, calculationEntries := range resp.Entries {
		if calculationEntries == nil {
			continue
		}
		calculationInfo := calculationEntries.Entries[commonstate.FakedContainerName]
		if calculationInfo == nil {
			continue
		}
		ownerPoolName, _ := resourcepackage.UnwrapOwnerPoolName(calculationInfo.OwnerPoolName)
		if commonstate.GetPoolType(ownerPoolName) != commonstate.PoolNameReclaim {
			continue
		}

		mandatoryResults := make(map[int64]*advisorapi.NumaCalculationResult)
		for numaID, result := range calculationInfo.CalculationResultsByNumas {
			if result == nil {
				continue
			}
			mandatoryBlocks := make([]*advisorapi.Block, 0, len(result.Blocks))
			for _, block := range result.Blocks {
				if block != nil && len(block.OverlapTargets) == 0 {
					mandatoryBlocks = append(mandatoryBlocks, block)
				}
			}
			if len(mandatoryBlocks) != 0 {
				mandatoryResults[numaID] = &advisorapi.NumaCalculationResult{Blocks: mandatoryBlocks}
			}
		}
		if len(mandatoryResults) == 0 {
			continue
		}
		mandatoryResp.Entries[entryName] = &advisorapi.CalculationEntries{
			Entries: map[string]*advisorapi.CalculationInfo{
				commonstate.FakedContainerName: {
					OwnerPoolName:             calculationInfo.OwnerPoolName,
					CalculationResultsByNumas: mandatoryResults,
				},
			},
		}
	}

	return buildAdvisorBlockDescriptors(
		mandatoryResp, cpuDetails, podEntries, rpPinnedCPUSet, nonReclaimableCPUSet)
}

type pendingAdvisorState struct {
	preCommitRevision             uint64
	entries                       state.PodEntries
	allowOverlap                  bool
	disableDedicated              bool
	enforceSteadyReclaim          bool
	residualFloor                 machine.CPUSet
	dynamicConfig                 *dynamicconfig.Configuration
	migrationCheckpointTransition steadyFakeNUMAMigrationCheckpointTransition
}

func (p *DynamicPolicy) pendingAdvisorStateMatchesCommitted(pending *pendingAdvisorState) bool {
	if p == nil || p.state == nil || pending == nil {
		return false
	}
	return pending.preCommitRevision == p.state.GetRevision() &&
		pending.allowOverlap == p.state.GetAllowSharedCoresOverlapReclaimedCores() &&
		pending.disableDedicated == p.state.GetDisableDedicatedCoresOverlapReclaimedCores() &&
		reflect.DeepEqual(pending.entries, p.state.GetPodEntries())
}

// applyBlocks prepares an advisor state transition without
// invoking allocation hooks, writing checkpoints, or mutating plugin state.
// and the logic contains three main steps
// 1. construct entries for dedicated containers and pools
// 2. ensure reclaimed pool exists
// 3. construct entries for shared and reclaimed containers
func defaultShareQuantityFromAdvisorResponse(resp *advisorapi.ListAndWatchResponse) (int, error) {
	if resp == nil {
		return 0, fmt.Errorf("default share advice response is nil")
	}
	entries, ok := resp.Entries[commonstate.PoolNameShare]
	if !ok || entries == nil || len(entries.Entries) != 1 {
		return 0, fmt.Errorf("default share advice must contain exactly one pool entry")
	}
	calculationInfo, ok := entries.Entries[commonstate.FakedContainerName]
	if !ok || calculationInfo == nil || calculationInfo.OwnerPoolName != commonstate.PoolNameShare {
		return 0, fmt.Errorf("default share advice must contain exactly one share pool entry")
	}
	if len(calculationInfo.CalculationResultsByNumas) != 1 {
		return 0, fmt.Errorf("default share advice must contain only faked numa id")
	}
	numaResult, ok := calculationInfo.CalculationResultsByNumas[commonstate.FakedNUMAID]
	if !ok || numaResult == nil {
		return 0, fmt.Errorf("default share advice must contain only faked numa id")
	}

	var quantity uint64
	for _, block := range numaResult.Blocks {
		if block != nil {
			quantity += block.Result
		}
	}
	q, err := general.CovertUInt64ToInt(quantity)
	if err != nil {
		return 0, fmt.Errorf("convert default share quantity %d to int: %w", quantity, err)
	}
	return q, nil
}

func advisorResponseHasFakeNUMAMandatoryReclaim(
	resp *advisorapi.ListAndWatchResponse,
) bool {
	if resp == nil || !resp.DisableDedicatedCoresOverlapReclaimedCores {
		return false
	}
	for _, calculationEntries := range resp.Entries {
		if calculationEntries == nil {
			continue
		}
		for _, calculationInfo := range calculationEntries.Entries {
			if calculationInfo == nil {
				continue
			}
			ownerPoolName, _ := resourcepackage.UnwrapOwnerPoolName(
				calculationInfo.OwnerPoolName)
			if commonstate.GetPoolType(ownerPoolName) != commonstate.PoolNameReclaim {
				continue
			}
			result := calculationInfo.CalculationResultsByNumas[commonstate.FakedNUMAID]
			if result == nil {
				continue
			}
			for _, block := range result.Blocks {
				if block != nil && len(block.OverlapTargets) == 0 {
					return true
				}
			}
		}
	}
	return false
}

func (p *DynamicPolicy) applyBlocksWithDynamicConfig(
	blockCPUSet advisorapi.BlockCPUSet,
	resp *advisorapi.ListAndWatchResponse,
	allowSharedCoresOverlapReclaimedCores bool,
	hardActive bool,
	dynamicConf *dynamicconfig.Configuration,
) (*pendingAdvisorState, error) {
	if resp == nil {
		return nil, fmt.Errorf("applyBlocks got nil resp")
	}

	stateRevision := p.state.GetRevision()
	curEntries := p.state.GetPodEntries()
	newEntries := make(state.PodEntries)
	var dedicatedCPUSet, pooledUnionDedicatedCPUSet machine.CPUSet
	defaultSharePlan := defaultShareMaterializationPlan{}
	if dynamicConf != nil &&
		dynamicConf.FillDefaultSharePoolWithNonReclaimCPUs {
		defaultSharePlan.enabled = true
		var err error
		defaultSharePlan.advisedQuantity, err = defaultShareQuantityFromAdvisorResponse(resp)
		if err != nil {
			return nil, err
		}
	}

	// Calculate from the candidate entry snapshot rather than the currently
	// published machine state. The latter may still describe the previous
	// placement while this advisor frame is being prepared.
	nonReclaimActualBindingNUMAs, err := p.nonReclaimActualBindingNUMAsFromCandidate(curEntries)
	if err != nil {
		return nil, err
	}

	// deal with blocks of dedicated_cores and pools
	dedicatedCPUSet, pooledUnionDedicatedCPUSet, err = p.buildAdvisorDedicatedPoolEntries(
		blockCPUSet, resp, curEntries, newEntries, defaultSharePlan.enabled,
	)
	if err != nil {
		return nil, err
	}

	// deal with interrupt pools and system exclusive pools
	if err := p.copyInterruptAndSystemPools(curEntries, newEntries); err != nil {
		return nil, err
	}

	if resp.DisableDedicatedCoresOverlapReclaimedCores {
		// In disjoint mode the planner owns the complete reclaim target. Do not
		// revise or fallback-expand it after planning.
		if newEntries.CheckPoolEmpty(commonstate.PoolNameReclaim) {
			return nil, fmt.Errorf("disjoint advisor response has no reclaim partition")
		}
	} else {
		// Legacy mode keeps the historical fallback behavior for NUMAs without
		// actual reclaimed NUMA binding.
		err := p.reviseReclaimPool(
			newEntries,
			nonReclaimActualBindingNUMAs,
			pooledUnionDedicatedCPUSet,
			allowSharedCoresOverlapReclaimedCores,
		)
		if err != nil {
			return nil, err
		}
	}

	currentMachineState := p.state.GetMachineState()
	rampUpReclaimFloor := machine.NewCPUSet()
	if hardActive && resp.DisableDedicatedCoresOverlapReclaimedCores {
		reclaimInfo := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
		if reclaimInfo == nil {
			return nil, fmt.Errorf("negotiated disjoint advisor result has no reclaim floor")
		}
		rampUpReclaimFloor = reclaimInfo.AllocationResult.Clone()
	} else if hardActive {
		// The floor follows the ramp-up domains recorded in the negotiated
		// newEntries (the derive function unions its own derivation with the
		// passed-in set, which is empty here because no further allocation is
		// entering mid-advisor).
		negotiatedRampUpDomains, dErr := newEntries.ActiveRampUpDomains(p.machine.machineInfo.CPUTopology)
		if dErr != nil {
			return nil, fmt.Errorf("resolve negotiated ramp-up domains for advisor floor: %w", dErr)
		}
		legacyFloor, err := p.deriveRampUpReclaimFloorForModeWithDynamicConfig(
			currentMachineState, newEntries, negotiatedRampUpDomains, false, dynamicConf)
		if err != nil {
			return nil, fmt.Errorf("derive reclaim floor for advisor ramp-up failed: %w", err)
		}
		rampUpReclaimFloor = legacyFloor
	}
	if defaultSharePlan.enabled {
		ownershipEntries, err := projectDefaultShareOwnershipEntries(curEntries, newEntries, resp)
		if err != nil {
			return nil, err
		}
		defaultSharePlan.eligibleCPUSet = p.buildDefaultShareEligibleCPUSet(
			newEntries, currentMachineState, rampUpReclaimFloor)
		if err := p.finalizeDefaultShareEntry(
			newEntries, ownershipEntries, defaultSharePlan.advisedQuantity, defaultSharePlan.eligibleCPUSet,
		); err != nil {
			return nil, err
		}
	}

	// calculate rampUpCPUs
	sharedBindingNUMAs, err := resp.GetSharedBindingNUMAs()
	if err != nil {
		return nil, fmt.Errorf("GetSharedBindingNUMAs failed with error: %v", err)
	}
	sharedBindingNUMACPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(sharedBindingNUMAs.UnsortedList()...)
	notAllocatablePoolsCPUs := state.GetUnitedPoolsCPUs(p.state.GetPodEntries(), state.IsForbiddenPool, commonstate.IsSystemPool)
	// rampUpCPUs include reclaim pool in NUMAs without NUMA_binding cpus
	rampUpCPUs := p.machine.machineInfo.CPUDetails.CPUs().
		Difference(p.config.reservedCPUs).
		Difference(dedicatedCPUSet).
		Difference(sharedBindingNUMACPUs).
		Difference(notAllocatablePoolsCPUs)
	rampUpCPUs = rampUpCPUs.Difference(rampUpReclaimFloor)

	rampUpCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, rampUpCPUs)
	if err != nil {
		return nil, fmt.Errorf("unable to calculate topologyAwareAssignments for rampUpCPUs, result cpuset: %s, error: %v",
			rampUpCPUs.String(), err)
	}

	// Unadvised shared-numa-binding ramp-up allocations keep their current
	// allocation; they are protected from the final reclaim pool by the bulkhead
	// partition view and are not rematerialized here.
	if err := p.applyAdvisorNonPoolContainerEntries(
		curEntries, newEntries, resp, rampUpCPUs, rampUpCPUsTopologyAwareAssignments,
		rampUpReclaimFloor, hardActive, nonReclaimActualBindingNUMAs,
	); err != nil {
		return nil, err
	}

	commitOverride, err := p.buildAdjustmentCommitOverrideFromPodEntriesWithDynamicConfig(
		newEntries,
		allowSharedCoresOverlapReclaimedCores,
		resp.DisableDedicatedCoresOverlapReclaimedCores,
		dynamicConf,
	)
	if err != nil {
		return nil, fmt.Errorf("build adjustment commit override from pod entries failed with error: %w", err)
	}
	if err := p.syncReclaimPoolWithAdjustmentCommitOverrideWithDynamicConfig(
		newEntries, commitOverride, dynamicConf, defaultSharePlan.eligibleCPUSet,
	); err != nil {
		return nil, fmt.Errorf("sync reclaim pool with adjustment commit override failed with error: %w", err)
	}
	if defaultSharePlan.enabled {
		defaultSharePlan.eligibleCPUSet = p.buildDefaultShareEligibleCPUSet(
			newEntries, currentMachineState, rampUpReclaimFloor)
		if err := p.finalizeDefaultShareEntry(
			newEntries, newEntries, defaultSharePlan.advisedQuantity, defaultSharePlan.eligibleCPUSet,
		); err != nil {
			return nil, err
		}
	}

	// The hard-partition invariant is enforced against the FINAL reclaim pool:
	// the adjustment commit override above may rewrite the reclaim pool after
	// ramp-up shared allocations were assigned rampUpCPUs, so the mutual
	// exclusion can only be restored here, before the pending advisor state is
	// materialized. This is deliberately gated on the dynamic configuration
	// (plus active ramp-up presence), not on the hardActive parameter: the
	// parameter only controls floor derivation for this attempt, while the
	// partition invariant itself follows the switch.
	if isRampUpReclaimHardPartitionEnabledWithConfig(dynamicConf) &&
		newEntries.HasActiveRampUp() {
		if err := p.rematerializeRampUpSharedAgainstFinalReclaim(newEntries); err != nil {
			return nil, fmt.Errorf("rematerialize ramp-up shared allocations against final reclaim pool failed with error: %w", err)
		}
	}

	return &pendingAdvisorState{
		preCommitRevision: stateRevision,
		entries:           newEntries,
		allowOverlap:      allowSharedCoresOverlapReclaimedCores,
		disableDedicated:  resp.DisableDedicatedCoresOverlapReclaimedCores,
		residualFloor:     rampUpReclaimFloor,
		dynamicConfig:     dynamicConf,
	}, nil
}

// buildAdvisorDedicatedPoolEntries walks the advisor response and constructs (or
// updates) dedicated_cores and pool entries in newEntries, accumulating the
// union dedicated CPUSets. It is the dedicated/pools stage of
// applyBlocksWithDynamicConfig.
func (p *DynamicPolicy) buildAdvisorDedicatedPoolEntries(
	blockCPUSet advisorapi.BlockCPUSet,
	resp *advisorapi.ListAndWatchResponse,
	curEntries state.PodEntries,
	newEntries state.PodEntries,
	skipSharePool bool,
) (dedicatedCPUSet machine.CPUSet, pooledUnionDedicatedCPUSet machine.CPUSet, err error) {
	dedicatedCPUSet = machine.NewCPUSet()
	pooledUnionDedicatedCPUSet = machine.NewCPUSet()
	for entryName, entry := range resp.Entries {
		if entryName == commonstate.PoolNameInterrupt {
			continue
		}
		if skipSharePool && entryName == commonstate.PoolNameShare {
			continue
		}

		for subEntryName, calculationInfo := range entry.Entries {
			if calculationInfo == nil {
				general.Warningf("got nil calculationInfo entry: %s, subEntry: %s", entryName, subEntryName)
				continue
			} else if !(subEntryName == commonstate.FakedContainerName || calculationInfo.OwnerPoolName == commonstate.PoolNameDedicated) {
				continue
			}

			// construct cpuset for this entry by union all blocks for it
			entryCPUSet, err := calculationInfo.GetCPUSet(entryName, subEntryName, blockCPUSet)
			if err != nil {
				return dedicatedCPUSet, pooledUnionDedicatedCPUSet, err
			}

			// transform cpuset into topologyAwareAssignments
			topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, entryCPUSet)
			if err != nil {
				return dedicatedCPUSet, pooledUnionDedicatedCPUSet, fmt.Errorf("unable to calculate topologyAwareAssignments for entry: %s, subEntry: %s, entry cpuset: %s, error: %v",
					entryName, subEntryName, entryCPUSet.String(), err)
			}

			// if allocation already exists, update them; otherwise, construct new a new one
			allocationInfo := curEntries[entryName][subEntryName].Clone()
			if allocationInfo == nil {
				// currently, cpu advisor can only create new pools,
				// all container entries or entries with owner pool name dedicated can't be created by cpu advisor
				if calculationInfo.OwnerPoolName == commonstate.PoolNameDedicated || subEntryName != commonstate.FakedContainerName {
					return dedicatedCPUSet, pooledUnionDedicatedCPUSet, fmt.Errorf("no-pool entry isn't found in plugin cache, entry: %s, subEntry: %s", entryName, subEntryName)
				} else if entryName != calculationInfo.OwnerPoolName {
					return dedicatedCPUSet, pooledUnionDedicatedCPUSet, fmt.Errorf("pool entryName: %s and OwnerPoolName: %s mismatch", entryName, calculationInfo.OwnerPoolName)
				}

				general.Infof("create new pool: %s cpuset result %s", entryName, entryCPUSet.String())
				allocationInfo = &state.AllocationInfo{
					AllocationMeta: commonstate.AllocationMeta{
						PodUid:        entryName,
						OwnerPoolName: entryName,
					},
					AllocationResult:                 entryCPUSet.Clone(),
					OriginalAllocationResult:         entryCPUSet.Clone(),
					TopologyAwareAssignments:         topologyAwareAssignments,
					OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
				}
			} else {
				general.Infof("entry: %s, subEntryName: %s cpuset allocation result transform from %s(size: %d) to %s(size: %d)",
					entryName, subEntryName,
					allocationInfo.AllocationResult.String(), allocationInfo.AllocationResult.Size(),
					entryCPUSet.String(), entryCPUSet.Size())

				allocationInfo.OwnerPoolName = calculationInfo.OwnerPoolName
				allocationInfo.AllocationResult = entryCPUSet.Clone()
				allocationInfo.OriginalAllocationResult = entryCPUSet.Clone()
				allocationInfo.TopologyAwareAssignments = topologyAwareAssignments
				allocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)
			}

			if newEntries[entryName] == nil {
				newEntries[entryName] = make(state.ContainerEntries)
			}
			newEntries[entryName][subEntryName] = allocationInfo
			pooledUnionDedicatedCPUSet = pooledUnionDedicatedCPUSet.Union(allocationInfo.AllocationResult)

			if allocationInfo.OwnerPoolName == commonstate.PoolNameDedicated {
				dedicatedCPUSet = dedicatedCPUSet.Union(allocationInfo.AllocationResult)
				general.Infof("try to apply dedicated_cores: %s/%s %s: %s",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String())
			} else {
				for numaID, cpus := range allocationInfo.TopologyAwareAssignments {
					general.Infof("try to apply pool %s numa %d: %s", allocationInfo.OwnerPoolName, numaID, cpus.String())
				}
			}
		}
	}
	return dedicatedCPUSet, pooledUnionDedicatedCPUSet, nil
}

// copyInterruptAndSystemPools carries the interrupt pool and every system
// exclusive pool over from the current entries into the new advisor frame.
func (p *DynamicPolicy) copyInterruptAndSystemPools(curEntries, newEntries state.PodEntries) error {
	if subEntry, ok := curEntries[commonstate.PoolNameInterrupt]; ok {
		newEntries[commonstate.PoolNameInterrupt] = make(state.ContainerEntries)
		if ai, ok := subEntry[commonstate.FakedContainerName]; ok && ai != nil {
			newEntries[commonstate.PoolNameInterrupt][commonstate.FakedContainerName] = ai.Clone()
		}
	}

	// deal with system exclusive pools
	for name, subEntry := range curEntries {
		if !commonstate.IsSystemPool(name) {
			continue
		}
		if _, exists := newEntries[name]; exists {
			return fmt.Errorf("system pool %s already exists", name)
		}
		newEntries[name] = make(state.ContainerEntries)
		if ai, ok := subEntry[commonstate.FakedContainerName]; ok && ai != nil {
			newEntries[name][commonstate.FakedContainerName] = ai.Clone()
		}
	}
	return nil
}

// applyAdvisorNonPoolContainerEntries replays the non-pool container allocations
// from the current entries into the new frame, applying shared/reclaimed/ramp-up
// placement. Unadvised shared-NUMA-binding ramp-up allocations keep their current
// allocation and are protected from the final reclaim pool by the bulkhead
// partition view.
func (p *DynamicPolicy) applyAdvisorNonPoolContainerEntries(
	curEntries state.PodEntries,
	newEntries state.PodEntries,
	resp *advisorapi.ListAndWatchResponse,
	rampUpCPUs machine.CPUSet,
	rampUpCPUsTopologyAwareAssignments map[int]machine.CPUSet,
	rampUpReclaimFloor machine.CPUSet,
	hardActive bool,
	nonReclaimActualBindingNUMAs machine.CPUSet,
) error {
	for podUID, containerEntries := range curEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

	containerLoop:
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Errorf("pod: %s, container: %s has nil allocationInfo", podUID, containerName)
				continue
			}

			if newEntries[podUID][containerName] != nil {
				continue
			}

			if newEntries[podUID] == nil {
				newEntries[podUID] = make(state.ContainerEntries)
			}
			newEntries[podUID][containerName] = allocationInfo.Clone()
			// adapt to old checkpoint without RequestQuantity property
			newEntries[podUID][containerName].RequestQuantity = p.getContainerRequestedCores(allocationInfo)

			switch allocationInfo.QoSLevel {
			case consts.PodAnnotationQoSLevelDedicatedCores:
				errMsg := fmt.Sprintf("dedicated_cores blocks aren't applied, pod: %s/%s, container: %s",
					allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				general.Errorf("%s", errMsg)
				return fmt.Errorf(errMsg)
			case consts.PodAnnotationQoSLevelSystemCores:
				poolCPUSet, topologyAwareAssignments, err := p.getSystemPoolCPUSetAndNumaAwareAssignments(newEntries, allocationInfo)
				if err != nil {
					return fmt.Errorf("pod: %s/%s, container: %s is system_cores, "+
						"getSystemPoolCPUSetAndNumaAwareAssignments failed with error: %v",
						allocationInfo.PodNamespace, allocationInfo.PodName,
						allocationInfo.ContainerName, err)
				}

				newEntries[podUID][containerName].AllocationResult = poolCPUSet
				newEntries[podUID][containerName].OriginalAllocationResult = poolCPUSet.Clone()
				newEntries[podUID][containerName].TopologyAwareAssignments = topologyAwareAssignments
				newEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)
			case consts.PodAnnotationQoSLevelSharedCores:
				_, advisorReturnedAllocation := resp.GetCalculationInfo(podUID, containerName)
				if allocationInfo.RampUp &&
					allocationInfo.CheckSharedNUMABinding() &&
					allocationInfo.OwnerPoolName == commonstate.EmptyOwnerPoolName &&
					!advisorReturnedAllocation {
					// Unadvised SNB ramp-up keeps its current allocation; the bulkhead
					// partition view protects its exact CPUs from the final reclaim pool,
					// so no preservation tri-state is needed here.
					general.Infof("pod: %s/%s container: %s is an unadvised shared numa-binding ramp-up allocation, preserve its current allocation",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
					continue containerLoop
				}

				ownerPoolName, err := p.getOwnerPoolNameFromAdvisor(allocationInfo, resp)
				if err != nil {
					return err
				}
				if allocationInfo.RampUp {
					general.Infof("pod: %s/%s container: %s is in ramp up, set its allocation result from %s to rampUpCPUs :%s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String(), rampUpCPUs.String())

					if rampUpCPUs.IsEmpty() {
						if err := validateEmptyRampUpCPUReuse(
							hardActive,
							allocationInfo.AllocationResult,
							rampUpReclaimFloor,
						); err != nil {
							return fmt.Errorf("pod: %s/%s container: %s %w",
								allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, err)
						}
						general.Warningf("rampUpCPUs is empty. pod: %s/%s container: %s reuses its allocation result: %s",
							allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.AllocationResult.String())
						continue containerLoop
					}

					newEntries[podUID][containerName].OwnerPoolName = commonstate.EmptyOwnerPoolName
					newEntries[podUID][containerName].AllocationResult = rampUpCPUs.Clone()
					newEntries[podUID][containerName].OriginalAllocationResult = rampUpCPUs.Clone()
					newEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
					newEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(rampUpCPUsTopologyAwareAssignments)
				} else {
					poolEntry, err := p.getAllocationPoolEntry(allocationInfo, ownerPoolName, newEntries)
					if err != nil {
						return err
					}

					if allocationInfo.CheckSharedNUMABinding() {
						poolEntry.QoSLevel = consts.PodAnnotationQoSLevelSharedCores
						// set SharedNUMABinding declarations to pool entry containing SharedNUMABinding containers,
						// in order to differentiate them from non-binding share cores pools during GetFilteredPoolsCPUSetMap.
						poolEntry.Annotations = general.MergeMap(poolEntry.Annotations, map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						})
					}

					general.Infof("put pod: %s/%s container: %s to pool: %s, set its allocation result from %s to %s",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, ownerPoolName, allocationInfo.AllocationResult.String(), poolEntry.AllocationResult.String())

					newEntries[podUID][containerName].OwnerPoolName = ownerPoolName
					newEntries[podUID][containerName].AllocationResult = poolEntry.AllocationResult.Clone()
					newEntries[podUID][containerName].OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
					newEntries[podUID][containerName].TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
					newEntries[podUID][containerName].OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
				}
			case consts.PodAnnotationQoSLevelReclaimedCores:
				ownerPoolName, err := p.getOwnerPoolNameFromAdvisor(allocationInfo, resp)
				if err != nil {
					return err
				}
				poolEntry, err := p.getAllocationPoolEntry(allocationInfo, ownerPoolName, newEntries)
				if err != nil {
					return err
				}

				err = p.updateReclaimAllocationResultByPoolEntry(newEntries[podUID][containerName], poolEntry, nonReclaimActualBindingNUMAs)
				if err != nil {
					return err
				}
			default:
				return fmt.Errorf("invalid qosLevel: %s for pod: %s/%s container: %s",
					allocationInfo.QoSLevel, allocationInfo.PodNamespace,
					allocationInfo.PodName, allocationInfo.ContainerName)
			}
		}
	}
	return nil
}

// rematerializeRampUpSharedAgainstFinalReclaim restores the hard-partition
// invariant (ramp-up shared allocations never overlap the final reclaim pool)
// against the reclaim pool as it exists AFTER the adjustment commit override
// has been applied. Plain (non-SNB) shared ramp-up allocations were assigned
// rampUpCPUs before the override rewrote the reclaim pool, so they may end up
// overlapping the final reclaim pool.
//
// Restoration is canonical, not a naive write-back of
// allocation.Difference(reclaim): the candidate set is only used as input, and
// the actual cpuset is re-selected through takeCoreAlignedCPUSet so the result
// stays whole-core, deterministic and NUMA-consistent with the candidate
// distribution. The re-layout is confined to the allocation's own remainder
// (candidate = allocation - reclaim) and never widens to a broader universe or
// re-picks by request quantity. Any configuration in which no complete physical
// core survives is rejected fail-closed instead of silently shrinking to a
// partial core.
//
// Shared-NUMA-binding (SNB) ramp-up allocations are skipped unconditionally here:
// the bulkhead partition view already protects their exact CPUs from the final
// reclaim pool (see BuildCPUSetPartitionView / ApplyTransientProtectedNonReclaim),
// so the final reclaim pool excludes their cpusets. A residual overlap for an SNB
// allocation is therefore a partition bug and is surfaced upstream as a typed
// failure by validateRampUpReclaimMutualExclusion rather than being papered over
// here.
func (p *DynamicPolicy) rematerializeRampUpSharedAgainstFinalReclaim(
	newEntries state.PodEntries,
) error {
	return p.rematerializeRampUpSharedAgainstReclaim(newEntries, p.latestAppliedReclaimCPUSet())
}

// latestAppliedReclaimCPUSet returns the reclaim cpuset most recently verified
// as physically applied by the bulkhead topology owner, or an empty set when no
// bulkhead manager is wired (tests, topology disabled before the first apply).
func (p *DynamicPolicy) latestAppliedReclaimCPUSet() machine.CPUSet {
	if p == nil || p.bulkhead.bulkheadManager == nil {
		return machine.NewCPUSet()
	}
	return p.bulkhead.bulkheadManager.LatestAppliedReclaim()
}

// rematerializeRampUpSharedAgainstReclaim is the testable core of
// rematerializeRampUpSharedAgainstFinalReclaim.
//
// The pending ramp-up allocation becomes the admission pending-protection scope
// of the bulkhead topology. If it claims a cpu that the applied reclaim cgroups
// still hold (the final reclaim already dropped it but the shrink has not been
// written yet), the topology must shrink reclaim before it can satisfy the
// protection, while the shrink itself is gated on the same protected pending
// set. That cycle surfaces as a bare fixed-point no-progress and holds every
// admission until the pending pods are removed. Excluding final ∪ applied
// reclaim keeps the protection reachable without any reclaim shrink. When that
// stricter exclusion leaves no complete physical core, fall back to the
// historical final-only exclusion instead of rejecting the allocation.
func (p *DynamicPolicy) rematerializeRampUpSharedAgainstReclaim(
	newEntries state.PodEntries,
	appliedReclaim machine.CPUSet,
) error {
	if p == nil || p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return nil
	}
	reclaimEntries := newEntries[commonstate.PoolNameReclaim]
	if reclaimEntries == nil {
		return nil
	}
	reclaimPool := reclaimEntries[commonstate.FakedContainerName]
	if reclaimPool == nil || reclaimPool.AllocationResult.IsEmpty() {
		return nil
	}
	finalReclaim := reclaimPool.AllocationResult
	// Applied reclaim is only an extra, preferred exclusion; it never widens the
	// allocation and never becomes the error baseline.
	excludeReclaim := finalReclaim.Union(appliedReclaim)

	for _, containerEntries := range newEntries {
		if containerEntries == nil || containerEntries.IsPoolEntry() {
			continue
		}
		for _, ai := range containerEntries {
			if ai == nil || !ai.RampUp || !ai.CheckShared() || ai.CheckReclaimed() {
				continue
			}
			// SNB ramp-up allocations are protected by the bulkhead partition view and
			// are rematerialized nowhere here; a residual overlap fails upstream.
			if ai.CheckSharedNUMABinding() {
				continue
			}

			overlap := ai.AllocationResult.Intersection(excludeReclaim)
			if overlap.IsEmpty() {
				// zero-churn: already mutually exclusive with the final and the
				// applied reclaim pool, nothing to rematerialize.
				continue
			}

			finalCandidate := ai.AllocationResult.Difference(finalReclaim)
			if finalCandidate.IsEmpty() {
				return fmt.Errorf("ramp-up shared allocation is completely covered by the final reclaim pool: "+
					"pod: %s/%s container: %s allocation: %s reclaim: %s",
					ai.PodNamespace, ai.PodName, ai.ContainerName,
					ai.AllocationResult.String(), finalReclaim.String())
			}

			// Prefer final ∪ applied; fall back to final-only when the stricter
			// exclusion leaves no complete physical core.
			excluded := excludeReclaim
			candidate := ai.AllocationResult.Difference(excludeReclaim)
			rematerialized := machine.NewCPUSet()
			if !candidate.IsEmpty() {
				rematerialized = takeCoreAlignedCPUSet(
					p.machine.machineInfo.CPUTopology, candidate, candidate, candidate.Size())
			}
			if rematerialized.IsEmpty() && !excludeReclaim.Equals(finalReclaim) {
				general.Warningf("ramp-up shared allocation of pod: %s/%s container: %s has no complete core "+
					"outside final ∪ applied reclaim %s; falling back to final reclaim %s only",
					ai.PodNamespace, ai.PodName, ai.ContainerName, excludeReclaim.String(), finalReclaim.String())
				excluded = finalReclaim
				candidate = finalCandidate
				rematerialized = takeCoreAlignedCPUSet(
					p.machine.machineInfo.CPUTopology, candidate, candidate, candidate.Size())
			}
			if rematerialized.IsEmpty() {
				return fmt.Errorf("no complete physical core remains for ramp-up shared allocation after "+
					"excluding both the final reclaim pool and the applied reclaim set (reclaim/floor conflict): "+
					"pod: %s/%s container: %s allocation: %s candidate: %s final reclaim: %s applied reclaim: %s",
					ai.PodNamespace, ai.PodName, ai.ContainerName,
					ai.AllocationResult.String(), candidate.String(), finalReclaim.String(), appliedReclaim.String())
			}
			if rematerialized.Equals(ai.AllocationResult) {
				// idempotent: the canonical selection reproduces the current
				// allocation, skip the write-back to avoid churn.
				continue
			}

			topologyAwareAssignments, err := machine.GetNumaAwareAssignments(
				p.machine.machineInfo.CPUTopology, rematerialized)
			if err != nil {
				return fmt.Errorf("unable to calculate topologyAwareAssignments for rematerialized "+
					"ramp-up shared allocation: pod: %s/%s container: %s cpuset: %s, error: %v",
					ai.PodNamespace, ai.PodName, ai.ContainerName,
					rematerialized.String(), err)
			}

			general.Infof("rematerialize ramp-up shared allocation of pod: %s/%s container: %s from %s to %s "+
				"to keep it mutually exclusive with the final reclaim pool: %s (excluded: %s, applied reclaim: %s)",
				ai.PodNamespace, ai.PodName, ai.ContainerName,
				ai.AllocationResult.String(), rematerialized.String(), finalReclaim.String(),
				excluded.String(), appliedReclaim.String())

			ai.AllocationResult = rematerialized
			ai.OriginalAllocationResult = rematerialized.Clone()
			ai.TopologyAwareAssignments = topologyAwareAssignments
			ai.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(topologyAwareAssignments)
		}
	}
	return nil
}

func projectDefaultShareOwnershipEntries(
	current, pending state.PodEntries,
	resp *advisorapi.ListAndWatchResponse,
) (state.PodEntries, error) {
	if resp == nil {
		return nil, fmt.Errorf("advisor response is nil during shared owner projection")
	}
	projectedCurrent := current.Clone()
	for podUID, containerEntries := range projectedCurrent {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil || allocationInfo.RampUp ||
				allocationInfo.QoSLevel != consts.PodAnnotationQoSLevelSharedCores {
				continue
			}
			calculationInfo, ok := resp.GetCalculationInfo(podUID, containerName)
			if !ok {
				continue
			}
			if calculationInfo == nil {
				return nil, fmt.Errorf(
					"nil calculation info for shared owner projection: pod=%s container=%s",
					podUID, containerName)
			}
			allocationInfo.OwnerPoolName = calculationInfo.OwnerPoolName
		}
	}
	return supplementPodEntriesWithCurrentNonPool(pending, projectedCurrent), nil
}

func validateEmptyRampUpCPUReuse(hardPartitionEnabled bool, oldAllocation, reclaimFloor machine.CPUSet) error {
	if !hardPartitionEnabled {
		return nil
	}
	overlap := oldAllocation.Intersection(reclaimFloor)
	if overlap.IsEmpty() {
		return nil
	}
	return fmt.Errorf("ramp-up allocation %s overlaps reclaim hard floor %s when recomputed rampUpCPUs is empty",
		oldAllocation.String(), reclaimFloor.String())
}

func (p *DynamicPolicy) commitPendingAdvisorState(
	pending *pendingAdvisorState,
	targets ...*advisorPostCommitTarget,
) error {
	if pending == nil {
		return fmt.Errorf("pending advisor state is nil")
	}
	var target *advisorPostCommitTarget
	if len(targets) > 0 {
		target = targets[0]
	}
	entries, _, err := p.commitPendingCPUPartitionForAdvisorTarget(pendingCPUPartition{
		expectedRevision:          pending.preCommitRevision,
		entries:                   pending.entries,
		allowOverlap:              pending.allowOverlap,
		disableDedicated:          pending.disableDedicated,
		persist:                   true,
		source:                    "advisor apply",
		requireCoreAlignedReclaim: pending.disableDedicated,
		enforceSteadyReclaim:      pending.enforceSteadyReclaim,
		residualFloor:             pending.residualFloor,
		dynamicConfig:             pending.dynamicConfig,
	}, target)
	if err == nil {
		pending.entries = entries
	}
	return err
}

func (p *DynamicPolicy) buildAdjustmentCommitOverrideFromPodEntries(
	newEntries state.PodEntries,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
) (*cpusetutil.CPUSetAdjustmentCommitOverride, error) {
	var dynamicConf *dynamicconfig.Configuration
	if p != nil && p.config.dynamicConfig != nil {
		dynamicConf = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	return p.buildAdjustmentCommitOverrideFromPodEntriesWithDynamicConfig(
		newEntries, allowSharedCoresOverlapReclaimedCores,
		disableDedicatedCoresOverlapReclaimedCores, dynamicConf)
}

func (p *DynamicPolicy) buildAdjustmentCommitOverrideFromPodEntriesWithDynamicConfig(
	newEntries state.PodEntries,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	dynamicConf *dynamicconfig.Configuration,
) (*cpusetutil.CPUSetAdjustmentCommitOverride, error) {
	if p == nil || p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil || len(newEntries) == 0 {
		return nil, nil
	}
	if allowSharedCoresOverlapReclaimedCores && !disableDedicatedCoresOverlapReclaimedCores {
		return nil, nil
	}
	if allowSharedCoresOverlapReclaimedCores && disableDedicatedCoresOverlapReclaimedCores {
		reclaimEntry := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
		if reclaimEntry == nil || reclaimEntry.AllocationResult.IsEmpty() {
			return nil, nil
		}
		dedicated := machine.NewCPUSet()
		for _, containerEntries := range newEntries {
			if containerEntries == nil || containerEntries.IsPoolEntry() {
				continue
			}
			for _, allocation := range containerEntries {
				if allocation != nil && allocation.CheckDedicated() {
					dedicated = dedicated.Union(allocation.AllocationResult)
				}
			}
		}
		reclaim, err := p.coreAlignedReclaimOverride(
			reclaimEntry.AllocationResult.Difference(dedicated),
			disableDedicatedCoresOverlapReclaimedCores,
		)
		if err != nil {
			return nil, err
		}
		return &cpusetutil.CPUSetAdjustmentCommitOverride{
			ReclaimEffective: reclaim,
			Source:           "advisor_pending_entries",
		}, nil
	}
	snapshot := newCPUSetAdjustmentStateSnapshot(p.state)
	if snapshot == nil {
		return nil, nil
	}
	snapshot.podEntries = newEntries.Clone()
	// The advisor response mode is the pending transaction's source of truth.
	// The checkpoint may still hold the previous mode until CommitAdvisorState
	// persists pod entries, machine state, and the mode atomically.
	snapshot.allowOverlap = allowSharedCoresOverlapReclaimedCores
	snapshot.disableDedicated = disableDedicatedCoresOverlapReclaimedCores
	var view *bulkheadmodel.DesiredView
	var pendingProjection map[string]map[string]struct{}
	if p.hardBulkheadPartitionValidationEnabledWithDynamicConfig(dynamicConf) {
		snapshotRampUpDomains, derr := snapshot.podEntries.ActiveRampUpDomains(p.machine.machineInfo.CPUTopology)
		if derr != nil {
			return nil, fmt.Errorf("resolve snapshot ramp-up domains for bulkhead view: %w", derr)
		}
		view, pendingProjection = bulkheadutils.BuildCPUSetPartitionView(
			snapshot,
			p.machine.machineInfo.CPUTopology,
			p.cpuSetPartitionViewOptionsWithDynamicConfig(
				snapshot, snapshotRampUpDomains, dynamicConf),
		)
	} else {
		var err error
		// Synchronous admission/commit must NOT discard the per-container
		// pending-shared-projection side-channel. Previously it was dropped (`_`),
		// so pending non-SNB shared RampUp CPUs folded exactly at the RPC boundary
		// and could empty a reclaim SMT pair, surfacing as a UUE. The gate
		// (HardPartition=false && !allowOverlap) is reflected by the snapshot mode.
		view, pendingProjection, err = bulkheadutils.BuildValidatedCPUSetPartitionView(
			snapshot,
			p.machine.machineInfo.CPUTopology,
			bulkheadutils.CPUSetPartitionViewOptions{},
		)
		if err != nil {
			return nil, err
		}
	}
	if view == nil {
		return nil, nil
	}
	reclaim, err := p.coreAlignedReclaimOverride(
		view.ReclaimEffective,
		disableDedicatedCoresOverlapReclaimedCores,
	)
	if err != nil {
		return nil, err
	}
	// Stage the per-container projection hint so the synchronous admission
	// adjustment round consumes the exact same choke point the RPC boundary
	// planned for. Union into any already-staged batch (never assign): two
	// concurrent admissions in a debounced batch must both survive. An empty
	// hint must not clear a previously staged batch. Consumed+cleared once by
	// the next adjustment round.
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.stagedPendingSharedProjection = bulkheadutils.UnionPendingSharedProjection(
		p.adjustment.stagedPendingSharedProjection, pendingProjection)
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	return &cpusetutil.CPUSetAdjustmentCommitOverride{
		ReclaimEffective:        reclaim,
		Source:                  "advisor_pending_entries",
		PendingSharedProjection: pendingProjection,
	}, nil
}

func (p *DynamicPolicy) coreAlignedReclaimOverride(
	reclaim machine.CPUSet,
	requireCoreAlignment bool,
) (machine.CPUSet, error) {
	if !requireCoreAlignment {
		return reclaim.Clone(), nil
	}
	if p == nil || p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return machine.NewCPUSet(), fmt.Errorf("cannot align reclaim override without cpu topology")
	}
	for _, cpu := range reclaim.ToSliceInt() {
		if _, ok := p.machine.machineInfo.CPUTopology.CPUDetails[cpu]; !ok {
			return machine.NewCPUSet(), fmt.Errorf("reclaim override cpu %d has no topology metadata", cpu)
		}
	}
	return takeCoreAlignedCPUSet(
		p.machine.machineInfo.CPUTopology,
		reclaim,
		reclaim,
		reclaim.Size(),
	), nil
}

func (p *DynamicPolicy) syncReclaimPoolWithAdjustmentCommitOverride(
	newEntries state.PodEntries,
	override *cpusetutil.CPUSetAdjustmentCommitOverride,
	defaultShareEligible ...machine.CPUSet,
) error {
	var dynamicConf *dynamicconfig.Configuration
	if p != nil && p.config.dynamicConfig != nil {
		dynamicConf = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	return p.syncReclaimPoolWithAdjustmentCommitOverrideWithDynamicConfig(
		newEntries, override, dynamicConf, defaultShareEligible...)
}

func (p *DynamicPolicy) syncReclaimPoolWithAdjustmentCommitOverrideWithDynamicConfig(
	newEntries state.PodEntries,
	override *cpusetutil.CPUSetAdjustmentCommitOverride,
	dynamicConf *dynamicconfig.Configuration,
	defaultShareEligible ...machine.CPUSet,
) error {
	if override == nil || (override.ReclaimEffective.IsEmpty() && override.Source == "") {
		return nil
	}
	reclaimEntry, ok := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	if !ok || reclaimEntry == nil {
		return nil
	}
	assignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, override.ReclaimEffective)
	if err != nil {
		return err
	}
	reclaim := override.ReclaimEffective.Clone()
	reclaimEntry.AllocationResult = reclaim
	reclaimEntry.OriginalAllocationResult = reclaim.Clone()
	reclaimEntry.TopologyAwareAssignments = assignments
	reclaimEntry.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(assignments)
	general.Infof("bulkhead: synced reclaim pool with adjustment commit override, source=%s reclaim=%s",
		override.Source, reclaim.String())
	if dynamicConf != nil {
		if dynamicConf.FillDefaultSharePoolWithNonReclaimCPUs {
			var eligible machine.CPUSet
			if len(defaultShareEligible) > 0 {
				eligible = defaultShareEligible[0].Clone()
			} else {
				eligible = p.buildDefaultShareEligibleCPUSet(
					newEntries, p.state.GetMachineState(), machine.NewCPUSet())
			}
			if err := p.finalizeDefaultShareEntryForMode(
				newEntries,
				newEntries,
				0,
				eligible,
				defaultShareMaterializationRecovery,
			); err != nil {
				return fmt.Errorf("sync default share with reclaim override: %w", err)
			}
		}
	}
	return p.syncReclaimAndShareAllocationsWithFinalPools(newEntries)
}

func (p *DynamicPolicy) syncReclaimAndShareAllocationsWithFinalPools(
	entries state.PodEntries,
) error {
	nonReclaimActualBindingNUMAs, err := p.nonReclaimActualBindingNUMAsFromCandidate(entries)
	if err != nil {
		return err
	}

	for podUID, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, allocation := range containerEntries {
			if allocation == nil || allocation.RampUp {
				continue
			}

			ownerPoolName := allocation.GetOwnerPoolName()
			poolEntry := entries[ownerPoolName].GetPoolEntry()
			switch {
			case allocation.CheckReclaimed() && ownerPoolName == commonstate.PoolNameReclaim:
				if poolEntry == nil {
					return fmt.Errorf("sync reclaim override owner %s/%s: reclaim pool entry is missing",
						podUID, containerName)
				}
				if err := p.updateReclaimAllocationResultByPoolEntry(
					allocation, poolEntry, nonReclaimActualBindingNUMAs,
				); err != nil {
					return fmt.Errorf("sync reclaim override owner %s/%s: %w", podUID, containerName, err)
				}
			case allocation.CheckShared() && ownerPoolName == commonstate.PoolNameShare:
				if poolEntry == nil {
					return fmt.Errorf("sync reclaim override owner %s/%s: default share pool entry is missing",
						podUID, containerName)
				}
				allocation.AllocationResult = poolEntry.AllocationResult.Clone()
				allocation.OriginalAllocationResult = poolEntry.OriginalAllocationResult.Clone()
				allocation.TopologyAwareAssignments = machine.DeepcopyCPUAssignment(poolEntry.TopologyAwareAssignments)
				allocation.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(
					poolEntry.OriginalTopologyAwareAssignments)
			}
		}
	}
	return nil
}

func (p *DynamicPolicy) nonReclaimActualBindingNUMAsFromCandidate(
	entries state.PodEntries,
) (machine.CPUSet, error) {
	if p == nil || p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return machine.NewCPUSet(), fmt.Errorf("derive candidate RNB topology: cpu topology is not initialized")
	}

	result := machine.NewCPUSet(p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt()...)
	for podUID, containerEntries := range entries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, allocation := range containerEntries {
			if allocation == nil || !allocation.CheckReclaimedActualNUMABinding() {
				continue
			}
			numaID, err := allocation.GetSpecifiedNUMABindingNUMAID()
			if err != nil {
				return machine.NewCPUSet(), fmt.Errorf(
					"derive candidate RNB topology for %s/%s: %w", podUID, containerName, err)
			}
			result = result.Difference(machine.NewCPUSet(numaID))
		}
	}
	return result, nil
}

func (p *DynamicPolicy) validateAdvisorPartitionBeforeCommitWithDynamicConfig(
	newEntries state.PodEntries,
	newMachineState state.NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	dynamicConf *dynamicconfig.Configuration,
) error {
	if p == nil || p.state == nil || p.machine.machineInfo == nil || p.machine.machineInfo.CPUTopology == nil {
		return nil
	}
	reclaimEntries := newEntries[commonstate.PoolNameReclaim]
	if reclaimEntries == nil {
		return nil
	}
	reclaimPool := reclaimEntries[commonstate.FakedContainerName]
	if reclaimPool == nil {
		return nil
	}

	for numaID, reclaimInNUMA := range reclaimPool.TopologyAwareAssignments {
		if numaID == commonstate.FakedNUMAID {
			continue
		}
		numaCPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID)
		if !reclaimInNUMA.IsSubsetOf(numaCPUs) {
			return fmt.Errorf("reclaim pool assignment crosses NUMA: numa=%d cpus=%s allowed=%s",
				numaID, reclaimInNUMA.String(), numaCPUs.String())
		}
	}

	// When DisableDedicatedCoresOverlapReclaimedCores is enabled, dedicated_cores
	// containers must not share any cpu with the reclaim pool, regardless of the
	// allowSharedCoresOverlapReclaimedCores switch.
	if disableDedicatedCoresOverlapReclaimedCores {
		notAllocatablePoolsCPUs := state.GetUnitedPoolsCPUs(
			newEntries,
			state.IsForbiddenPool,
			commonstate.IsSystemPool,
		)
		for _, containerEntries := range newEntries {
			if containerEntries == nil || containerEntries.IsPoolEntry() {
				continue
			}
			for _, ai := range containerEntries {
				if ai == nil || !ai.CheckDedicated() {
					continue
				}
				if overlap := reclaimPool.AllocationResult.Intersection(ai.AllocationResult); !overlap.IsEmpty() {
					return fmt.Errorf("dedicated_cores pod: %s/%s container: %s overlaps reclaim pool while DisableDedicatedCoresOverlapReclaimedCores enabled: %s",
						ai.PodNamespace, ai.PodName, ai.ContainerName, overlap.String())
				}
				if !ai.CheckDedicatedNUMABindingNUMAExclusive() {
					continue
				}
				for numaID := range ai.OriginalTopologyAwareAssignments {
					eligible := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID).
						Difference(p.config.reservedCPUs).
						Difference(notAllocatablePoolsCPUs)
					covered := ai.AllocationResult.Intersection(eligible).
						Union(reclaimPool.AllocationResult.Intersection(eligible))
					if !covered.Equals(eligible) {
						return fmt.Errorf("exclusive dedicated/reclaim partition does not cover eligible NUMA: pod=%s/%s container=%s numa=%d dedicated=%s reclaim=%s eligible=%s missing=%s",
							ai.PodNamespace, ai.PodName, ai.ContainerName, numaID,
							ai.AllocationResult.Intersection(eligible).String(),
							reclaimPool.AllocationResult.Intersection(eligible).String(),
							eligible.String(),
							eligible.Difference(covered).String())
					}
				}
			}
		}
	}

	// Ramp-up shared allocations are governed by the ramp-up reclaim hard
	// partition switch (HP), not by allowSharedCoresOverlapReclaimedCores (AO):
	// when HP is active, they must stay mutually exclusive with the FINAL
	// reclaim pool regardless of AO. This runs BEFORE the AO early-return below
	// so the HP=true && AO=true quadrant is covered as well; applyBlocks has
	// already rematerialized the restorable overlaps before commit, so an
	// overlap reaching this point fails closed.
	hpActive := isRampUpReclaimHardPartitionEnabledWithConfig(dynamicConf) && newEntries.HasActiveRampUp()
	if hpActive {
		if err := validateRampUpReclaimMutualExclusion(newEntries, dynamicConf); err != nil {
			return err
		}
	}

	if allowSharedCoresOverlapReclaimedCores {
		return p.validatePendingAdvisorPartitionViewWithDynamicConfig(
			newEntries,
			newMachineState,
			allowSharedCoresOverlapReclaimedCores,
			disableDedicatedCoresOverlapReclaimedCores,
			dynamicConf,
		)
	}

	disallowedSharedPartition := machine.NewCPUSet()
	for name, containerEntries := range newEntries {
		if containerEntries == nil {
			continue
		}
		if containerEntries.IsPoolEntry() {
			if name == commonstate.PoolNameReclaim {
				continue
			}
			if commonstate.IsSystemPool(name) {
				continue
			}
			if commonstate.GetPoolType(name) == commonstate.PoolNameDedicated &&
				!disableDedicatedCoresOverlapReclaimedCores {
				continue
			}
			if ai := containerEntries[commonstate.FakedContainerName]; ai != nil {
				disallowedSharedPartition = disallowedSharedPartition.Union(ai.AllocationResult)
			}
			continue
		}
		for _, ai := range containerEntries {
			if ai == nil {
				continue
			}
			// system_cores containers are never part of the disallowed shared
			// partition: bulkhead's BuildCPUSetPartitionView never folds them
			// into NonReclaimPool, so they may legally overlap the reclaim pool
			// (e.g. when a missing specified pool triggers whole-machine
			// fallback). Exclude them here to keep this always-on invariant
			// consistent with the bulkhead partition view.
			if ai.CheckReclaimed() || ai.CheckSystem() {
				continue
			}
			// Ramp-up shared allocations are exclusively governed by
			// validateRampUpReclaimMutualExclusion above: when the hard
			// partition switch is off (hpActive == false), their overlap with
			// the reclaim pool is legal; when it is on, any overlap has
			// already been rejected above (applyBlocks rematerialized the
			// restorable ones before commit), so exclude them here to avoid
			// duplicate errors. Steady shared allocations keep flowing into
			// the disallowed partition below, untouched.
			if isRampUpShared := ai.RampUp && ai.CheckShared() && !ai.CheckReclaimed(); isRampUpShared {
				continue
			}
			if ai.CheckDedicated() && !disableDedicatedCoresOverlapReclaimedCores {
				continue
			}
			disallowedSharedPartition = disallowedSharedPartition.Union(ai.AllocationResult)
		}
	}
	if overlap := reclaimPool.AllocationResult.Intersection(disallowedSharedPartition); !overlap.IsEmpty() {
		return fmt.Errorf("reclaim pool overlaps disallowed shared partition before commit: %s", overlap.String())
	}
	return p.validatePendingAdvisorPartitionViewWithDynamicConfig(
		newEntries,
		newMachineState,
		allowSharedCoresOverlapReclaimedCores,
		disableDedicatedCoresOverlapReclaimedCores,
		dynamicConf,
	)
}

// validateRampUpReclaimMutualExclusion enforces the ramp-up reclaim hard
// partition invariant: when the hard partition switch is active (and ramp-up
// allocations are actually present), every ramp-up shared allocation must be
// mutually exclusive with the FINAL reclaim pool, regardless of
// allowSharedCoresOverlapReclaimedCores. It is self-contained on purpose so
// the asynchronous cpuset adjustment path can stack it on top of the bulkhead
// view validation without inheriting the full unified guard (which would be
// wrong under an AO flip window).
func validateRampUpReclaimMutualExclusion(
	newEntries state.PodEntries,
	dynamicConf *dynamicconfig.Configuration,
) error {
	if !isRampUpReclaimHardPartitionEnabledWithConfig(dynamicConf) || !newEntries.HasActiveRampUp() {
		return nil
	}
	reclaimEntries := newEntries[commonstate.PoolNameReclaim]
	if reclaimEntries == nil {
		return nil
	}
	reclaimPool := reclaimEntries[commonstate.FakedContainerName]
	if reclaimPool == nil {
		return nil
	}

	for _, containerEntries := range newEntries {
		if containerEntries == nil || containerEntries.IsPoolEntry() {
			continue
		}
		for _, ai := range containerEntries {
			if ai == nil || !ai.RampUp || !ai.CheckShared() || ai.CheckReclaimed() {
				continue
			}
			if overlap := reclaimPool.AllocationResult.Intersection(ai.AllocationResult); !overlap.IsEmpty() {
				return fmt.Errorf("ramp-up shared allocation overlaps the final reclaim pool while "+
					"ramp-up reclaim hard partition is enabled: pod: %s/%s container: %s "+
					"allocation: %s reclaim: %s overlap: %s",
					ai.PodNamespace, ai.PodName, ai.ContainerName,
					ai.AllocationResult.String(), reclaimPool.AllocationResult.String(), overlap.String())
			}
		}
	}
	return nil
}

// validatePendingAdvisorPartitionViewWithRampUpExclusionWithDynamicConfig is the
// attempt-scoped validator: it pins the ramp-up/reclaim mutual-exclusion gate to
// the immutable *Configuration captured at the outer attempt entry, so a commit
// cannot mix a floor sized against generation N with a gate evaluated against
// generation N+1 swapped in mid-flight.
func (p *DynamicPolicy) validatePendingAdvisorPartitionViewWithRampUpExclusionWithDynamicConfig(
	newEntries state.PodEntries,
	newMachineState state.NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	dynamicConf *dynamicconfig.Configuration,
) error {
	if err := p.validatePendingAdvisorPartitionViewWithDynamicConfig(
		newEntries, newMachineState, allowSharedCoresOverlapReclaimedCores,
		disableDedicatedCoresOverlapReclaimedCores, dynamicConf,
	); err != nil {
		return err
	}
	return validateRampUpReclaimMutualExclusion(newEntries, dynamicConf)
}

func (p *DynamicPolicy) validatePendingAdvisorPartitionView(
	newEntries state.PodEntries,
	newMachineState state.NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
) error {
	var dynamicConf *dynamicconfig.Configuration
	if p != nil && p.config.dynamicConfig != nil {
		dynamicConf = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	return p.validatePendingAdvisorPartitionViewWithDynamicConfig(
		newEntries, newMachineState, allowSharedCoresOverlapReclaimedCores,
		disableDedicatedCoresOverlapReclaimedCores, dynamicConf)
}

func (p *DynamicPolicy) validatePendingAdvisorPartitionViewWithDynamicConfig(
	newEntries state.PodEntries,
	newMachineState state.NUMANodeMap,
	allowSharedCoresOverlapReclaimedCores bool,
	disableDedicatedCoresOverlapReclaimedCores bool,
	dynamicConf *dynamicconfig.Configuration,
) error {
	if !p.hardBulkheadPartitionValidationEnabledWithDynamicConfig(dynamicConf) {
		return nil
	}
	snapshot := newCPUSetAdjustmentStateSnapshot(p.state)
	snapshot.podEntries = newEntries.Clone()
	snapshot.machineState = newMachineState.Clone()
	snapshot.allowOverlap = allowSharedCoresOverlapReclaimedCores
	snapshot.disableDedicated = disableDedicatedCoresOverlapReclaimedCores
	commitViewRampUpDomains, derr := newEntries.ActiveRampUpDomains(p.machine.machineInfo.CPUTopology)
	if derr != nil {
		return fmt.Errorf("resolve commit-time ramp-up domains for bulkhead view: %w", derr)
	}
	if _, _, err := bulkheadutils.BuildValidatedCPUSetPartitionView(
		snapshot,
		p.machine.machineInfo.CPUTopology,
		p.cpuSetPartitionViewOptionsWithDynamicConfig(
			snapshot, commitViewRampUpDomains, dynamicConf),
	); err != nil {
		return fmt.Errorf("validate pending advisor partition view before commit: %w", err)
	}
	return nil
}

func (p *DynamicPolicy) cpuSetPartitionViewOptionsWithDynamicConfig(
	state state.ReadonlyState,
	rampUpDomains sets.Int,
	dynamicConf *dynamicconfig.Configuration,
) bulkheadutils.CPUSetPartitionViewOptions {
	var coreConf *config.Configuration
	var topology *machine.CPUTopology
	if p != nil {
		coreConf = p.config.conf
		if p.machine.machineInfo != nil {
			topology = p.machine.machineInfo.CPUTopology
		}
	}
	reservedCPUs := machine.NewCPUSet()
	reservedReclaimedCPUs := machine.NewCPUSet()
	reservedReclaimedCPUsFallback := 0
	if p != nil {
		reservedCPUs = p.config.reservedCPUs.Clone()
		reservedReclaimedCPUs = p.reclaim.reservedReclaimedCPUSet.Clone()
		reservedReclaimedCPUsFallback = p.reclaim.reservedReclaimedCPUsSize
	}
	return bulkheadutils.NewCPUSetPartitionViewOptionsWithState(
		coreConf,
		dynamicConf,
		topology,
		bulkheadutils.CPUSetPartitionViewState{
			State:                         state,
			ReservedCPUs:                  reservedCPUs,
			ReservedReclaimedCPUs:         reservedReclaimedCPUs,
			ReservedReclaimedCPUsFallback: reservedReclaimedCPUsFallback,
		},
		rampUpDomains,
	)
}

func (p *DynamicPolicy) hardBulkheadPartitionValidationEnabled() bool {
	var dynamicConf *dynamicconfig.Configuration
	if p != nil && p.config.dynamicConfig != nil {
		dynamicConf = p.config.dynamicConfig.GetDynamicConfiguration()
	}
	return p.hardBulkheadPartitionValidationEnabledWithDynamicConfig(dynamicConf)
}

func (p *DynamicPolicy) hardBulkheadPartitionValidationEnabledWithDynamicConfig(
	dynamicConf *dynamicconfig.Configuration,
) bool {
	if dynamicConf == nil || dynamicConf.AdminQoSConfiguration == nil ||
		dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	cpuConf := dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration
	return cpuConf.BulkheadConfig.Enable &&
		dynamicConf.EnableReclaim &&
		cpuConf.EnableRampUpReclaimHardPartition
}

func (p *DynamicPolicy) getOwnerPoolNameFromAdvisor(
	allocationInfo *state.AllocationInfo,
	resp *advisorapi.ListAndWatchResponse,
) (string, error) {
	ownerPoolName := allocationInfo.GetOwnerPoolName()
	if calculationInfo, ok := resp.GetCalculationInfo(allocationInfo.PodUid, allocationInfo.ContainerName); ok {
		if calculationInfo == nil {
			return "", fmt.Errorf("nil calculation info for pod %s container %s",
				allocationInfo.PodUid, allocationInfo.ContainerName)
		}
		general.Infof("cpu advisor put pod: %s/%s, container: %s from %s to %s",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, ownerPoolName, calculationInfo.OwnerPoolName)

		ownerPoolName = calculationInfo.OwnerPoolName
	} else {
		general.Warningf("cpu advisor doesn't return entry for pod: %s/%s, container: %s, qosLevel: %s",
			allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, allocationInfo.QoSLevel)
	}
	return ownerPoolName, nil
}

type numaHeadroomPermitWriter interface {
	SetNUMAHeadroomWithPermit(map[int]float64, bool, *state.WritePermit) error
}

func (p *DynamicPolicy) applyHeadroom(resp *advisorapi.ListAndWatchResponse) error {
	if p.applyHeadroomHook != nil {
		return p.applyHeadroomHook(resp)
	}
	if resp == nil {
		return fmt.Errorf("applyHeadroom got nil resp")
	}
	target := p.currentAdvisorPostCommitTarget()

	for _, calculationInfo := range resp.ExtraEntries {
		if calculationInfo == nil {
			general.Warningf("resp.ExtraEntries has nil calculationInfo")
			continue
		} else if calculationInfo.CalculationResult == nil {
			general.Warningf("resp.ExtraEntries has nil CalculationResult")
			continue
		}

		if err := p.applyNUMAHeadroom(calculationInfo, target); err != nil {
			return err
		}
	}

	return nil
}

func (p *DynamicPolicy) applyNUMAHeadroom(
	calculationInfo *advisorsvc.CalculationInfo,
	target *advisorPostCommitTarget,
) error {
	v, ok := calculationInfo.CalculationResult.Values[string(advisorapi.ControlKnobKeyCPUNUMAHeadroom)]
	if !ok {
		return nil
	}

	cpuNUMAHeadroom := &advisorapi.CPUNUMAHeadroom{}
	if err := json.Unmarshal([]byte(v), cpuNUMAHeadroom); err != nil {
		return fmt.Errorf("unmarshal %s: %s failed with error: %v",
			advisorapi.ControlKnobKeyCPUNUMAHeadroom, v, err)
	}

	var err error
	if target == nil {
		err = p.state.SetNUMAHeadroom(*cpuNUMAHeadroom, true)
	} else if writer, ok := p.state.(numaHeadroomPermitWriter); ok {
		err = writer.SetNUMAHeadroomWithPermit(
			*cpuNUMAHeadroom, true, p.newAdvisorStateWritePermit(target))
	} else {
		err = fmt.Errorf("state does not support permitted NUMA headroom writes")
	}
	if err != nil {
		return fmt.Errorf("persist cpu NUMA headroom: %w", err)
	}
	general.Infof("cpuNUMAHeadroom: %v", cpuNUMAHeadroom)
	return nil
}

// committedReclaimNUMABucket returns the reclaim pool's committed NUMA bucket
// from the currently published state. The second return reports whether a
// committed bucket actually existed for the NUMA (comma-ok), so a missing key is
// never confused with an initialized-empty bucket.
func (p *DynamicPolicy) committedReclaimNUMABucket(numaID int) (machine.CPUSet, bool) {
	committed := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	if committed == nil || committed.TopologyAwareAssignments == nil {
		return machine.NewCPUSet(), false
	}
	bucket, ok := committed.TopologyAwareAssignments[numaID]
	if !ok {
		return machine.NewCPUSet(), false
	}
	return bucket, true
}

func (p *DynamicPolicy) reviseReclaimPool(
	newEntries state.PodEntries,
	nonReclaimActualBindingNUMAs, pooledUnionDedicatedCPUSet machine.CPUSet,
	allowSharedCoresOverlapReclaimedCores bool,
) error {
	// The advisor candidate must never promote a reclaimed_cores container in
	// ramp-up. Reject it before backfilling buckets so an old advisor/async
	// candidate cannot bypass the invariant.
	if err := validateNoReclaimedRampUp(newEntries); err != nil {
		return err
	}
	notAllocatablePoolsCPUs := state.GetUnitedPoolsCPUs(newEntries, state.IsForbiddenPool, commonstate.IsSystemPool)
	nonReclaimCPUSet := pooledUnionDedicatedCPUSet.Union(notAllocatablePoolsCPUs)

	// Ensure the reclaim pool entry exists when the advisor omits its block.
	// Only overlap mode may fall back to CPUs already assigned to non-reclaim entries.
	if newEntries.CheckPoolEmpty(commonstate.PoolNameReclaim) {
		reclaimPoolCPUSet := p.machine.machineInfo.CPUDetails.CPUs().Difference(p.config.reservedCPUs).Difference(pooledUnionDedicatedCPUSet).Difference(notAllocatablePoolsCPUs)
		if !allowSharedCoresOverlapReclaimedCores {
			reclaimPoolCPUSet = takeCoreAlignedCPUSet(
				p.machine.machineInfo.CPUTopology,
				reclaimPoolCPUSet,
				p.reclaim.reservedReclaimedCPUSet,
				reclaimPoolCPUSet.Size(),
			)
		}
		if reclaimPoolCPUSet.IsEmpty() && allowSharedCoresOverlapReclaimedCores {
			reclaimPoolCPUSet = p.reclaim.reservedReclaimedCPUSet.Clone()
			general.Infof("fallback takeByNUMABalance for reclaimPoolCPUSet: %s", reclaimPoolCPUSet.String())
		}

		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, reclaimPoolCPUSet)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, "+
				"result cpuset: %s, error: %v", commonstate.PoolNameReclaim, reclaimPoolCPUSet.String(), err)
		}

		if newEntries[commonstate.PoolNameReclaim] == nil {
			newEntries[commonstate.PoolNameReclaim] = make(state.ContainerEntries)
		}
		newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName] = &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        commonstate.PoolNameReclaim,
				OwnerPoolName: commonstate.PoolNameReclaim,
			},
			AllocationResult:                 reclaimPoolCPUSet.Clone(),
			OriginalAllocationResult:         reclaimPoolCPUSet.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		}

		general.Infof("set reclaim allocationResult: %s, reclaim topologyAwareAssignments: %v",
			newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult.String(),
			newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].TopologyAwareAssignments)
	} else {
		general.Infof("detected reclaim allocationResult: %s, reclaim topologyAwareAssignments: %v",
			newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].AllocationResult.String(),
			newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName].TopologyAwareAssignments)
	}

	reclaimPool := newEntries[commonstate.PoolNameReclaim][commonstate.FakedContainerName]
	// Reuse the shared ownership view for the active RNB target NUMAs instead of a
	// second scan of the candidate.
	ownershipView, err := buildReclaimOwnershipView(newEntries, p.machine.machineInfo.CPUTopology)
	if err != nil {
		return err
	}
	activeRNBTargets := ownershipView.activeRNBTargetNUMAs
	selectFallback := func(numaID int) (machine.CPUSet, error) {
		// CPUs on this NUMA that are actually allocatable for reclaim right now.
		available := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID).
			Difference(p.config.reservedCPUs).
			Difference(nonReclaimCPUSet)

		preference, reservedOK := p.reclaim.reservedReclaimedTopologyAwareAssignments[numaID]
		var want int
		if reservedOK {
			want = preference.Size()
		} else {
			// Reserved reclaim preference is absent for this NUMA (map miss).
			// Safely reuse the already-committed reclaim bucket as the preference
			// and requested size, so a transient reserved-config gap does not
			// silently shrink the committed reclaim partition. The CPUs actually
			// selected are still drawn from `available`, so a CPU that has since
			// become reserved or non-reclaim is never re-added.
			committed, committedOK := p.committedReclaimNUMABucket(numaID)
			if committedOK {
				preference = committed
				want = committed.Size()
			} else {
				preference = machine.NewCPUSet()
				want = 0
			}
		}
		if allowSharedCoresOverlapReclaimedCores {
			// Overlap mode intentionally preserves the reserved/committed
			// preference where it overlaps dedicated/shared cores. It must still be
			// on-NUMA and must never reintroduce CPUs now owned by a forbidden or
			// system pool, so intersect with the NUMA topology and subtract those
			// non-allocatable pool classes (dedicated/shared CPUs are intentionally
			// kept: that is the allowed overlap).
			overlapAllowed := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID).
				Difference(notAllocatablePoolsCPUs)
			return preference.Intersection(overlapAllowed), nil
		}

		selected := takeCoreAlignedCPUSet(
			p.machine.machineInfo.CPUTopology, available, preference, want)
		if selected.Size() != want {
			return machine.NewCPUSet(), fmt.Errorf(
				"select core-aligned reclaim fallback in NUMA %d: got %d, want %d",
				numaID, selected.Size(), want)
		}
		return selected, nil
	}

	// revise reclaim pool for RNB NUMAs
	for _, numaID := range p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
		if nonReclaimActualBindingNUMAs.Contains(numaID) {
			continue
		}

		if reclaimPool.TopologyAwareAssignments[numaID].IsEmpty() {
			fallbackCPUSet, err := selectFallback(numaID)
			if err != nil {
				return err
			}
			if fallbackCPUSet.IsEmpty() {
				if _, active := activeRNBTargets[numaID]; active {
					// An active (non-ramp-up) RNB allocation is pinned to this
					// NUMA, so the reclaim pool must back it. Neither the reserved
					// preference nor the filtered committed bucket could supply a
					// CPU: fail closed instead of leaving the pod unprovisioned.
					return fmt.Errorf(
						"reclaim pool cannot back active reclaimed-numa-binding on NUMA %d: "+
							"no reserved preference and no usable committed fallback",
						numaID)
				}
				// No active RNB here: leave the bucket absent rather than
				// materializing an initialized-empty entry that normalize would
				// later sparsify away.
				continue
			}
			reclaimPool.AllocationResult = reclaimPool.AllocationResult.Union(fallbackCPUSet)
			reclaimPool.OriginalAllocationResult = reclaimPool.OriginalAllocationResult.Union(fallbackCPUSet)
			reclaimPool.TopologyAwareAssignments[numaID] = fallbackCPUSet.Clone()
			reclaimPool.OriginalTopologyAwareAssignments[numaID] = fallbackCPUSet.Clone()
		}
	}

	// revise reclaim pool for non-RNB NUMAs
	nonReclaimActualBindingNUMAsAllocation := machine.NewCPUSet()
	for _, numaID := range p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
		if nonReclaimActualBindingNUMAs.Contains(numaID) {
			nonReclaimActualBindingNUMAsAllocation = nonReclaimActualBindingNUMAsAllocation.Union(reclaimPool.TopologyAwareAssignments[numaID])
		}
	}

	// when the advisor has not assigned any reclaim CPUs to non-RNB NUMAs, fall back to
	// the pre-computed reservedReclaimedTopologyAwareAssignments for those NUMAs so that
	// the reclaim pool is never empty. Iterate the non-RNB NUMA IDs, not the (already
	// empty) cpu set, otherwise this branch is dead code.
	if nonReclaimActualBindingNUMAsAllocation.IsEmpty() {
		for _, numaID := range p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt() {
			if !nonReclaimActualBindingNUMAs.Contains(numaID) {
				continue
			}
			fallbackCPUSet, err := selectFallback(numaID)
			if err != nil {
				return err
			}
			if fallbackCPUSet.IsEmpty() {
				// No reclaim CPU available for this NUMA; leave the bucket absent
				// instead of materializing an initialized-empty entry.
				continue
			}
			reclaimPool.AllocationResult = reclaimPool.AllocationResult.Union(fallbackCPUSet)
			reclaimPool.OriginalAllocationResult = reclaimPool.OriginalAllocationResult.Union(fallbackCPUSet)
			reclaimPool.TopologyAwareAssignments[numaID] = fallbackCPUSet.Clone()
			reclaimPool.OriginalTopologyAwareAssignments[numaID] = fallbackCPUSet.Clone()
		}
	}

	general.Infof("revised reclaim allocationResult: %s, reclaim topologyAwareAssignments: %v",
		reclaimPool.AllocationResult.String(), reclaimPool.TopologyAwareAssignments)
	return nil
}
