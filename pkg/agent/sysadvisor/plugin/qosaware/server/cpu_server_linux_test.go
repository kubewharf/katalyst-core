//go:build linux
// +build linux

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

package server

import (
	"context"
	"encoding/json"
	"flag"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/opencontainers/runc/libcontainer/configs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func TestCPUServerUpdate(t *testing.T) {
	t.Parallel()

	cgResource := configs.Resources{CpuQuota: -1, CpuPeriod: 100000}
	tmp, err := json.Marshal(&cgResource)
	assert.NoError(t, err)
	defaultCgroupConfig := string(tmp)

	flagSet := flag.NewFlagSet("test", flag.ExitOnError)
	klog.InitFlags(flagSet)
	_ = flagSet.Parse([]string{"--v", "6"})
	defer klog.InitFlags(nil)

	type testCase struct {
		name      string
		provision types.InternalCPUCalculationResult
		infos     []*ContainerInfo
		wantRes   *cpuadvisor.ListAndWatchResponse
	}

	tests := []testCase{
		{
			name: "reclaim pool with shared pool",
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameShare:                       {-1: {Size: 2, Quota: common.CPUQuotaUnlimit}},
					commonstate.PoolNameReclaim:                     {-1: {Size: 4, Quota: common.CPUQuotaUnlimit}},
					commonstate.PoolNamePrefixIsolation + "-test-1": {-1: {Size: 4, Quota: common.CPUQuotaUnlimit}},
				},
			},
			infos: []*ContainerInfo{
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c1",
						QosLevel:      consts.PodAnnotationQoSLevelSharedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey: consts.PodAnnotationQoSLevelSharedCores,
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c1",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameShare,
					},
					isolated: true,
					regions:  sets.NewString(commonstate.PoolNamePrefixIsolation + "-test-1"),
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-0",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-1",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameShare: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameShare,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNamePrefixIsolation + "-test-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},

					"pod1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"c1": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool with dedicated pod",
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReclaim: {
						0: {Size: 4, Quota: -1},
						1: {Size: 8, Quota: -1},
					},
				},
			},
			infos: []*ContainerInfo{
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c1",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c1",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-0",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-1",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
								},
							},
						},
					},
					"pod1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"c1": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result:         16,
												OverlapTargets: nil,
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool colocated with dedicated pod(2 containers)",
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReclaim: {
						0: {Size: 4, Quota: common.CPUQuotaUnlimit},
						1: {Size: 8, Quota: common.CPUQuotaUnlimit},
					},
				},
			},
			infos: []*ContainerInfo{
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c1",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c2",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c2",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c2",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-0",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-1",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
								},
							},
						},
					},
					"pod1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"c1": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
							"c2": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool colocated with dedicated pod(3 containers)",
			provision: types.InternalCPUCalculationResult{
				TimeStamp: time.Now(),
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReclaim: {
						0: {Size: 4, Quota: common.CPUQuotaUnlimit},
						1: {Size: 8, Quota: common.CPUQuotaUnlimit},
					},
				},
			},
			infos: []*ContainerInfo{
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c1",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c1",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c2",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c2",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
				{
					request: &advisorsvc.ContainerMetadata{
						PodUid:        "pod1",
						ContainerName: "c3",
						Annotations: map[string]string{
							consts.PodAnnotationMemoryEnhancementNumaBinding: consts.PodAnnotationMemoryEnhancementNumaBindingEnable,
						},
						QosLevel: consts.PodAnnotationQoSLevelDedicatedCores,
					},
					podInfo: &v1.Pod{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: "default",
							Name:      "pod1",
							UID:       "pod1",
							Annotations: map[string]string{
								consts.PodAnnotationQoSLevelKey:          consts.PodAnnotationQoSLevelDedicatedCores,
								consts.PodAnnotationMemoryEnhancementKey: "{\"numa_exclusive\":true}",
							},
						},
						Spec: v1.PodSpec{
							Containers: []v1.Container{
								{
									Name: "c3",
								},
							},
						},
					},
					allocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameDedicated,
						TopologyAwareAssignments: map[uint64]string{
							0: "0-3",
							1: "24-47",
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-0",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-1",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
										},
									},
								},
							},
						},
					},
					"pod1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"c1": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
							"c2": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c3",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
							"c3": {
								OwnerPoolName: commonstate.PoolNameDedicated,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									0: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
									1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 16,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
												},
											},
											{
												Result: 8,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c1",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPodUid:        "pod1",
														OverlapTargetContainerName: "c2",
														OverlapType:                cpuadvisor.OverlapType_OverlapWithPod,
													},
													{
														OverlapTargetPoolName: commonstate.PoolNameReclaim,
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
													},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool overlap shared pool[1]",
			provision: types.InternalCPUCalculationResult{
				AllowSharedCoresOverlapReclaimedCores: true,
				TimeStamp:                             time.Now(),
				PoolEntries: map[string]map[int]types.CPUResource{
					strings.Join([]string{commonstate.PoolNameShare, "1"}, "-"): {-1: {Size: 2, Quota: common.CPUQuotaUnlimit}},
					strings.Join([]string{commonstate.PoolNameShare, "2"}, "-"): {-1: {Size: 2, Quota: common.CPUQuotaUnlimit}},
					commonstate.PoolNameReclaim:                                 {-1: {Size: 4, Quota: common.CPUQuotaUnlimit}},
					commonstate.PoolNamePrefixIsolation + "-test-1":             {-1: {Size: 4, Quota: common.CPUQuotaUnlimit}},
				},
				PoolOverlapInfo: map[string]map[int]map[string]int{
					commonstate.PoolNameReclaim: {
						-1: map[string]int{
							"share-1": 2,
							"share-2": 2,
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: true,
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-0",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-1",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					"share-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: "share-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "reclaim",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					"share-2": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: "share-2",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "reclaim",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "share-1",
													},
												},
											},
											{
												Result: 2,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "share-2",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNamePrefixIsolation + "-test-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool overlap shared pool[2]",
			provision: types.InternalCPUCalculationResult{
				AllowSharedCoresOverlapReclaimedCores: true,
				TimeStamp:                             time.Now(),
				PoolEntries: map[string]map[int]types.CPUResource{
					strings.Join([]string{commonstate.PoolNameShare, "1"}, "-"): {-1: {Size: 4, Quota: common.CPUQuotaUnlimit}},
					strings.Join([]string{commonstate.PoolNameShare, "2"}, "-"): {-1: {Size: 4, Quota: common.CPUQuotaUnlimit}},
					commonstate.PoolNameReclaim:                                 {-1: {Size: 2, Quota: common.CPUQuotaUnlimit}},
					commonstate.PoolNamePrefixIsolation + "-test-1":             {-1: {Size: 4, Quota: common.CPUQuotaUnlimit}},
				},
				PoolOverlapInfo: map[string]map[int]map[string]int{
					commonstate.PoolNameReclaim: {
						-1: map[string]int{
							"share-1": 1,
							"share-2": 1,
						},
					},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: true,
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-0",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-1",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					"share-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: "share-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 1,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "reclaim",
													},
												},
											},
											{
												Result: 3,
											},
										},
									},
								},
							},
						},
					},
					"share-2": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: "share-2",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 1,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "reclaim",
													},
												},
											},
											{
												Result: 3,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 1,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "share-1",
													},
												},
											},
											{
												Result: 1,
												OverlapTargets: []*cpuadvisor.OverlapTarget{
													{
														OverlapType:           cpuadvisor.OverlapType_OverlapWithPool,
														OverlapTargetPoolName: "share-2",
													},
												},
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNamePrefixIsolation + "-test-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "reclaim pool with isolated pool",
			provision: types.InternalCPUCalculationResult{
				AllowSharedCoresOverlapReclaimedCores: true,
				TimeStamp:                             time.Now(),
				PoolEntries: map[string]map[int]types.CPUResource{
					commonstate.PoolNameReclaim:                     {-1: {Size: 2, Quota: common.CPUQuotaUnlimit}},
					commonstate.PoolNamePrefixIsolation + "-test-1": {-1: {Size: 4, Quota: common.CPUQuotaUnlimit}},
				},
			},
			wantRes: &cpuadvisor.ListAndWatchResponse{
				AllowSharedCoresOverlapReclaimedCores: true,
				ExtraEntries: []*advisorsvc.CalculationInfo{
					{
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cpu_numa_headroom": "{}"},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-0",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
					{
						CgroupPath: "/kubepods/besteffort-1",
						CalculationResult: &advisorsvc.CalculationResult{
							Values: map[string]string{"cgroup_config": defaultCgroupConfig},
						},
					},
				},
				Entries: map[string]*cpuadvisor.CalculationEntries{
					commonstate.PoolNameReclaim: {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNameReclaim,
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 2,
											},
										},
									},
								},
							},
						},
					},
					commonstate.PoolNamePrefixIsolation + "-test-1": {
						Entries: map[string]*cpuadvisor.CalculationInfo{
							"": {
								OwnerPoolName: commonstate.PoolNamePrefixIsolation + "-test-1",
								CalculationResultsByNumas: map[int64]*cpuadvisor.NumaCalculationResult{
									-1: {
										Blocks: []*cpuadvisor.Block{
											{
												Result: 4,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	testWithListAndWatch := func(
		t *testing.T,
		advisor *mockCPUResourceAdvisor,
		cs *cpuServer,
		tt testCase,
	) {
		// populate checkpoint
		checkpoint := &cpuadvisor.GetCheckpointResponse{
			Entries: make(map[string]*cpuadvisor.AllocationEntries),
		}
		checkpoint.Entries[commonstate.PoolNameReserve] = &cpuadvisor.AllocationEntries{
			Entries: map[string]*cpuadvisor.AllocationInfo{
				commonstate.FakedContainerName: {
					OwnerPoolName: commonstate.PoolNameReserve,
				},
			},
		}
		for _, info := range tt.infos {
			if _, ok := checkpoint.Entries[info.request.PodUid]; !ok {
				checkpoint.Entries[info.request.PodUid] = &cpuadvisor.AllocationEntries{
					Entries: make(map[string]*cpuadvisor.AllocationInfo),
				}
			}
			checkpoint.Entries[info.request.PodUid].Entries[info.request.ContainerName] = info.allocationInfo
		}

		// start mock qrm server
		qrmServer := &mockQRMCPUPluginServer{
			checkpoint: checkpoint,
			err:        nil,
		}
		server := grpc.NewServer()
		cpuadvisor.RegisterCPUPluginServer(server, qrmServer)

		sock, err := net.Listen("unix", cs.pluginSocketPath)
		require.NoError(t, err)
		defer sock.Close()
		go func() {
			server.Serve(sock)
		}()

		// populate MetaCache
		for _, info := range tt.infos {
			assert.NoError(t, cs.addContainer(info.request))
			assert.NoError(t, cs.updateContainerInfo(info.request.PodUid, info.request.ContainerName, info.podInfo, info.allocationInfo))

			nodeInfo, _ := cs.metaCache.GetContainerInfo(info.request.PodUid, info.request.ContainerName)
			nodeInfo.Isolated = info.isolated
			if info.regions.Len() > 0 {
				nodeInfo.RegionNames = info.regions
			}
			assert.NoError(t, cs.metaCache.SetContainerInfo(info.request.PodUid, info.request.ContainerName, nodeInfo))
		}

		s := &mockCPUServerService_ListAndWatchServer{ResultsChan: make(chan *cpuadvisor.ListAndWatchResponse)}
		stop := make(chan struct{})
		go func() {
			err := cs.ListAndWatch(&advisorsvc.Empty{}, s)
			assert.NoError(t, err)
			close(stop)
		}()

		res := <-s.ResultsChan
		close(cs.stopCh)
		<-stop

		res, err = DeepCopyResponse(res)
		assert.NoError(t, err)
		assert.ElementsMatch(t, tt.wantRes.ExtraEntries, res.ExtraEntries)
		assert.Equal(t, tt.wantRes.Entries, res.Entries)
		assert.Equal(t, tt.wantRes.AllowSharedCoresOverlapReclaimedCores, res.AllowSharedCoresOverlapReclaimedCores)
	}

	testWithGetAdvice := func(
		t *testing.T,
		advisor *mockCPUResourceAdvisor,
		cs *cpuServer,
		tt testCase,
	) {
		// populate GetAdviceRequest
		request := &cpuadvisor.GetAdviceRequest{
			Entries: map[string]*cpuadvisor.ContainerAllocationInfoEntries{},
		}
		request.Entries[commonstate.PoolNameReserve] = &cpuadvisor.ContainerAllocationInfoEntries{
			Entries: map[string]*cpuadvisor.ContainerAllocationInfo{
				commonstate.FakedContainerName: {
					AllocationInfo: &cpuadvisor.AllocationInfo{
						OwnerPoolName: commonstate.PoolNameReserve,
					},
				},
			},
		}
		for _, info := range tt.infos {
			if _, ok := request.Entries[info.request.PodUid]; !ok {
				request.Entries[info.request.PodUid] = &cpuadvisor.ContainerAllocationInfoEntries{
					Entries: make(map[string]*cpuadvisor.ContainerAllocationInfo),
				}
			}
			request.Entries[info.request.PodUid].Entries[info.request.ContainerName] = &cpuadvisor.ContainerAllocationInfo{
				Metadata: &advisorsvc.ContainerMetadata{
					PodUid:        info.request.PodUid,
					PodNamespace:  info.podInfo.Namespace,
					PodName:       info.podInfo.Name,
					ContainerName: info.request.ContainerName,
					Annotations:   info.request.Annotations,
					QosLevel:      info.request.QosLevel,
				},
				AllocationInfo: &cpuadvisor.AllocationInfo{
					RampUp:                           info.allocationInfo.RampUp,
					OwnerPoolName:                    info.allocationInfo.OwnerPoolName,
					TopologyAwareAssignments:         info.allocationInfo.TopologyAwareAssignments,
					OriginalTopologyAwareAssignments: info.allocationInfo.OriginalTopologyAwareAssignments,
				},
			}
		}
		advisor.onUpdate = func() {
			for _, info := range tt.infos {
				ci, ok := cs.metaCache.GetContainerInfo(info.request.PodUid, info.request.ContainerName)
				// container info should have been populated
				assert.True(t, ok)
				ci.Isolated = info.isolated
				if info.regions.Len() > 0 {
					ci.RegionNames = info.regions
				}
				assert.NoError(t, cs.metaCache.SetContainerInfo(info.request.PodUid, info.request.ContainerName, ci))
			}
		}

		resp, err := cs.GetAdvice(context.Background(), request)
		assert.NoError(t, err)
		lwResp, err := DeepCopyResponse(&cpuadvisor.ListAndWatchResponse{
			Entries:                               resp.Entries,
			AllowSharedCoresOverlapReclaimedCores: resp.AllowSharedCoresOverlapReclaimedCores,
			ExtraEntries:                          resp.ExtraEntries,
		})
		assert.NoError(t, err)

		assert.ElementsMatch(t, resp.ExtraEntries, lwResp.ExtraEntries)
		assert.Equal(t, tt.wantRes.Entries, lwResp.Entries)
		assert.Equal(t, tt.wantRes.AllowSharedCoresOverlapReclaimedCores, lwResp.AllowSharedCoresOverlapReclaimedCores)
	}

	for _, tt := range tests {
		tt := tt
		// reuse the test cases and test both ListAndWatch and GetAdvice
		for apiMode, testFunc := range map[string]func(*testing.T, *mockCPUResourceAdvisor, *cpuServer, testCase){
			"ListAndWatch": testWithListAndWatch,
			"GetAdvice":    testWithGetAdvice,
		} {
			testFunc := testFunc
			t.Run(tt.name+"_"+apiMode, func(t *testing.T) {
				t.Parallel()

				advisor := &mockCPUResourceAdvisor{
					provision: &tt.provision,
					err:       nil,
				}
				var pods []*v1.Pod
				for _, info := range tt.infos {
					pods = append(pods, info.podInfo)
				}
				cs := newTestCPUServer(t, advisor, pods)

				testFunc(t, advisor, cs, tt)
			})
		}
	}
}
