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

package handler

import (
	rawContext "context"
	"fmt"
	"path/filepath"
	"testing"

	. "github.com/bytedance/mockey"
	. "github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/fake"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/types"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	pciAnnotationKey = "pci-devices"
)

func generateStateReconciler(t *testing.T, vfState state.VFState, podEntries state.PodEntries) *StateReconciler {
	tmpDir := t.TempDir()
	stateImpl, err := state.NewCheckpointState(nil, &global.MachineInfoConfiguration{}, &statedirectory.StateDirectoryConfiguration{
		StateFileDirectory:         filepath.Join(tmpDir, "state_file"),
		InMemoryStateFileDirectory: filepath.Join(tmpDir, "state_memory"),
		EnableInMemoryState:        false,
	}, "checkpoint", "sriov", true, metrics.DummyMetrics{})
	require.NoError(t, err)
	stateImpl.SetMachineState(vfState, false)
	stateImpl.SetPodEntries(podEntries, false)
	err = stateImpl.StoreState()
	require.NoError(t, err)

	require.NoError(t, err)

	return &StateReconciler{
		state:           stateImpl,
		netnsDirAbsPath: "/sys",
		pciAnnotation:   pciAnnotationKey,
		resourceName:    string(apiconsts.ResourceSriovNic),
		kubeClient:      fake.NewSimpleClientset(),
		residualHitMap:  make(map[string]int64),
	}
}

func TestStateReconciler_syncMachineState(t *testing.T) {
	t.Parallel()

	PatchConvey("syncMachineState", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)
		vfState[0].ExtraVFInfo = nil
		vfState[1].ExtraVFInfo = nil

		reconciler := generateStateReconciler(t, vfState, podEntries)

		Mock((*state.VFInfo).InitExtraInfo).To(func(vf *state.VFInfo, _ string) error {
			vf.ExtraVFInfo = &state.ExtraVFInfo{
				Name:       fmt.Sprintf("vfName-%d", vf.Index),
				QueueCount: 8,
				IBDevices:  []string{fmt.Sprintf("umad%d", vf.Index), fmt.Sprintf("uverbs%d", vf.Index)},
			}
			return nil
		}).Build()

		needStore, err := reconciler.syncMachineState(nil)

		So(err, ShouldBeNil)
		So(needStore, ShouldBeTrue)
		So(reconciler.state.GetMachineState()[0].ExtraVFInfo, ShouldNotBeNil)
		So(reconciler.state.GetMachineState()[1].ExtraVFInfo, ShouldNotBeNil)
	})
}

func TestStateReconciler_addMissingAllocationInfo(t *testing.T) {
	t.Parallel()

	Convey("syncMachineState", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, nil)

		reconciler := generateStateReconciler(t, vfState, podEntries)

		mockPodUID := "mock-pod-uid"
		mockContainerName := "mock-container-name"
		targetVF := vfState[3]

		podFetcher := &pod.PodFetcherStub{
			PodList: []*corev1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						UID: apitypes.UID(mockPodUID),
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: mockContainerName,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceName(reconciler.resourceName): resource.MustParse("1"),
									},
								},
							},
						},
					},
				},
			},
		}

		needStore, err := reconciler.addMissingAllocationInfo(map[string]types.PCIDevice{
			mockPodUID: {
				Address: targetVF.PCIAddr,
				RepName: targetVF.RepName,
				VFName:  targetVF.PFName,
			},
		}, &metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: podFetcher}})
		So(err, ShouldBeNil)
		So(needStore, ShouldBeTrue)

		allocationInfo := reconciler.state.GetAllocationInfo(mockPodUID, mockContainerName)

		So(allocationInfo, ShouldNotBeNil)
		So(allocationInfo.VFInfo, ShouldResemble, targetVF)
	})
}

func TestStateReconciler_deleteAbsentAllocationInfo(t *testing.T) {
	t.Parallel()

	Convey("syncMachineState", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0),
		})

		reconciler := generateStateReconciler(t, vfState, podEntries)

		needStore, err := reconciler.deleteAbsentAllocationInfo(&metaserver.MetaServer{
			MetaAgent: &agent.MetaAgent{
				PodFetcher: &pod.PodFetcherStub{
					PodList: nil,
				},
			},
		})

		So(err, ShouldBeNil)
		So(needStore, ShouldBeFalse)
		So(reconciler.residualHitMap, ShouldResemble, map[string]int64{"pod0": 1})
	})
}

func TestStateReconciler_updatePodSriovVFResultAnnotation(t *testing.T) {
	t.Parallel()

	Convey("updatePodSriovVFResultAnnotation", t, func() {
		vfState, podEntries := state.GenerateDummyState(2, 2, map[int]sets.Int{
			0: sets.NewInt(0),
		})

		reconciler := generateStateReconciler(t, vfState, podEntries)

		mockPod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				UID:  apitypes.UID("pod0"),
				Name: "pod0",
			},
		}

		podFetcher := &pod.PodFetcherStub{
			PodList: []*corev1.Pod{mockPod},
		}

		reconciler.kubeClient = fake.NewSimpleClientset(mockPod)

		err := reconciler.updatePodSriovVFResultAnnotation(&metaserver.MetaServer{MetaAgent: &agent.MetaAgent{PodFetcher: podFetcher}})
		So(err, ShouldBeNil)

		updatedPod, err := reconciler.kubeClient.CoreV1().Pods(mockPod.Namespace).Get(rawContext.Background(), mockPod.Name, metav1.GetOptions{})
		So(err, ShouldBeNil)
		So(updatedPod.Annotations[apiconsts.PodAnnotationSriovVFResultKey], ShouldEqual, "eth0_0")
	})
}
