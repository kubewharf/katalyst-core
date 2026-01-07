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

package util

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes"
	cri "k8s.io/cri-api/pkg/apis"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type StateReconciler struct {
	state          state.State
	pciAnnotation  string
	kubeClient     kubernetes.Interface
	runtimeClient  cri.RuntimeService
	metaServer     *metaserver.MetaServer
	residualHitMap map[string]int64
}

func NewStateReconciler(state state.State, pciAnnotation string,
	kubeClient kubernetes.Interface, runtimeClient cri.RuntimeService,
) *StateReconciler {
	return &StateReconciler{
		state:          state,
		pciAnnotation:  pciAnnotation,
		kubeClient:     kubeClient,
		runtimeClient:  runtimeClient,
		residualHitMap: make(map[string]int64),
	}
}

func (r *StateReconciler) Reconcile(_ *config.Configuration,
	_ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration,
	_ metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
) {
	var errList []error

	defer func() {
		err := errors.NewAggregate(errList)
		if err != nil {
			general.ErrorS(err, "failed to reconcile state")
		}
		_ = general.UpdateHealthzStateByError(consts.HealthzReconcileState, err)
	}()

	runtimePodPCIDevice, runtimePCIAddressSet, err := r.getRuntimePodPCIDevice()
	if err != nil {
		errList = append(errList, fmt.Errorf("failed to get pod vf: %w", err))
		return
	}

	machineStateUpdated, err := r.syncMachineState(runtimePCIAddressSet)
	if err != nil {
		errList = append(errList, fmt.Errorf("failed to sync machine state: %w", err))
	}

	allocationInfoDeleted, err := r.deleteAbsentAllocationInfo(metaServer)
	if err != nil {
		errList = append(errList, fmt.Errorf("failed to delete absent allocation info: %w", err))
	}

	allocationInfoAdded, err := r.addMissingAllocationInfo(runtimePodPCIDevice, metaServer)
	if err != nil {
		errList = append(errList, fmt.Errorf("failed to add missing allocation info: %w", err))
	}

	if err := r.updatePodSriovVFResultAnnotation(metaServer); err != nil {
		errList = append(errList, fmt.Errorf("failed to update pod sriov vf result annotation: %w", err))
		return
	}

	if !(machineStateUpdated && allocationInfoDeleted && allocationInfoAdded) {
		return
	}

	err = r.state.StoreState()
	if err != nil {
		errList = append(errList, fmt.Errorf("failed to store state: %w", err))
		return
	}
}

func (r *StateReconciler) getRuntimePodPCIDevice() (map[string]PCIDevice, sets.String, error) {
	sandboxes, err := r.runtimeClient.ListPodSandbox(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list pod sandboxes: %w", err)
	}

	podVF := make(map[string]PCIDevice)
	vfSet := sets.NewString()
	for _, sandbox := range sandboxes {
		pciDeviceStr, ok := sandbox.Annotations[r.pciAnnotation]
		if !ok {
			continue
		}

		pciDevices := make([]PCIDevice, 0)
		if err := json.Unmarshal([]byte(pciDeviceStr), &pciDevices); err != nil {
			general.Warningf("failed to unmarshal pci device from pod sandbox %s: %v", sandbox.Metadata.Uid, err)
			continue
		}

		if len(pciDevices) != 1 {
			general.Warningf("invalid pci device from pod sandbox %s: %s", sandbox.Metadata.Uid, pciDeviceStr)
			continue
		}

		device := pciDevices[0]

		podVF[sandbox.Metadata.Uid] = device
		vfSet.Insert(device.Address)
	}

	return podVF, vfSet, nil
}

func (r *StateReconciler) syncMachineState(allocatedVFSet sets.String) (bool, error) {
	errList := make([]error, 0)
	needStore := false

	machineState := r.state.GetMachineState()
	for _, vfInfo := range machineState {
		if vfInfo.ExtraVFInfo != nil {
			continue
		}
		if allocatedVFSet.Has(vfInfo.PCIAddr) {
			continue
		}
		if err := vfInfo.InitExtraInfo(vfInfo.PCIAddr); err != nil {
			errList = append(errList, fmt.Errorf("failed to init extra info of %s: %w", vfInfo.RepName, err))
			continue
		}
		general.Infof("init extra info of %s: %v", vfInfo.RepName, vfInfo.ExtraVFInfo)
		needStore = true
	}
	if needStore {
		r.state.SetMachineState(machineState, true)
	}

	return needStore, nil
}

func (r *StateReconciler) deleteAbsentAllocationInfo(metaServer *metaserver.MetaServer) (bool, error) {
	podList, err := metaServer.GetPodList(context.Background(), nil)
	if err != nil {
		return false, fmt.Errorf("failed to get pod list: %w", err)
	}
	podSet := sets.NewString()
	for _, pod := range podList {
		podSet.Insert(fmt.Sprintf("%v", pod.UID))
	}

	residualSet := make(map[string]bool)
	podEntries := r.state.GetPodEntries()
	for podUID := range podEntries {
		if !podSet.Has(podUID) {
			residualSet[podUID] = true
			r.residualHitMap[podUID] += 1
			general.Infof("found pod: %s with state but doesn't show up in pod watcher, hit count: %d", podUID, r.residualHitMap[podUID])
		}
	}

	podsToDelete := sets.NewString()
	for podUID, hitCount := range r.residualHitMap {
		if !residualSet[podUID] {
			general.Infof("already found pod: %s in pod watcher or its state is cleared, delete it from residualHitMap", podUID)
			delete(r.residualHitMap, podUID)
			continue
		}

		if time.Duration(hitCount)*consts.ReconcileStatePeriod >= consts.MaxResidualTime {
			podsToDelete.Insert(podUID)
		}
	}

	for podUID := range podsToDelete {
		r.state.Delete(podUID, false)
	}

	return len(podsToDelete) > 0, nil
}

func (r *StateReconciler) addMissingAllocationInfo(
	runtimePodPCIDevice map[string]PCIDevice, metaServer *metaserver.MetaServer,
) (bool, error) {
	needStore := false
	errList := make([]error, 0)
	machineState := r.state.GetMachineState()
	for podUID, pciDevice := range runtimePodPCIDevice {
		podEntries := r.state.GetPodEntries()
		if podEntries[podUID] != nil {
			continue
		}

		pod, err := metaServer.GetPod(context.Background(), podUID)
		if err != nil {
			errList = append(errList, fmt.Errorf("failed to get pod %s: %w", podUID, err))
			continue
		}

		containerName := getContainerWithSriovRequestOrFirst(pod)

		vfState := machineState.Filter(state.FilterByPCIAddr(pciDevice.Address))
		if len(vfState) != 1 {
			errList = append(errList, fmt.Errorf("failed to get vf by PCI address %T for pod %s", vfState, podUID))
			continue
		}

		allocationInfo := &state.AllocationInfo{
			AllocationMeta: commonstate.AllocationMeta{
				PodUid:        string(pod.UID),
				PodNamespace:  pod.Namespace,
				PodName:       pod.Name,
				ContainerName: containerName,
			},
			VFInfo: vfState[0],
		}
		general.Infof("set allocation info of %s: %v", containerName, allocationInfo)
		r.state.SetAllocationInfo(podUID, containerName, allocationInfo, false)
		needStore = true
	}

	return needStore, nil
}

func getContainerWithSriovRequestOrFirst(pod *corev1.Pod) string {
	var firstMainContainerName string
	for _, container := range pod.Spec.Containers {
		for resourceName := range container.Resources.Requests {
			if resourceName == apiconsts.ResourceSriovNic {
				return container.Name
			}
		}

		if firstMainContainerName == "" {
			firstMainContainerName = container.Name
		}
	}

	return firstMainContainerName
}

func (r *StateReconciler) updatePodSriovVFResultAnnotation(metaServer *metaserver.MetaServer) error {
	errList := make([]error, 0)
	podEntries := r.state.GetPodEntries()
	for podUID, podEntry := range podEntries {
		pod, err := metaServer.GetPod(context.Background(), podUID)
		if err != nil {
			errList = append(errList, fmt.Errorf("failed to get pod %s: %w", podUID, err))
			continue
		}
		if pod.Annotations[apiconsts.PodAnnotationSriovVFResultKey] != "" {
			continue
		}

		for _, allocationInfo := range podEntry {
			if err := UpdateSriovVFResultAnnotation(r.kubeClient, allocationInfo); err != nil {
				errList = append(errList, fmt.Errorf(""))
				return fmt.Errorf("failed to update sriov vf result annotation of %s/%s: %w", podUID, pod.Name, err)
			}
			general.Infof("updated sriov vf result annotation of %s/%s", podUID, pod.Name)
			break
		}
	}

	return nil
}
