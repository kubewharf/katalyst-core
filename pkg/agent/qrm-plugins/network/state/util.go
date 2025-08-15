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

package state

import (
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// GenerateMachineState returns NICResourcesMap based on
// machine info and reserved resources
func GenerateMachineState(conf *qrm.QRMPluginsConfiguration, nics []machine.InterfaceInfo, reservation map[string]uint32) (NICMap, error) {
	defaultMachineState := make(NICMap)
	for _, iface := range nics {
		reservedBandwidth := reservation[iface.Name]
		egressCapacity := uint32(float32(iface.Speed) * conf.EgressCapacityRate)
		ingressCapacity := uint32(float32(iface.Speed) * conf.IngressCapacityRate)

		generalLog := general.LoggerWithPrefix("GenerateMachineState", general.LoggingPKGFull)
		generalLog.Infof("NIC %s's speed: %d, capacity: [%d/%d], reservation: %d", iface.Name, iface.Speed, egressCapacity, ingressCapacity, reservedBandwidth)

		// we do not differentiate egress reservation and ingress reservation for now. That is, they are supposed to be the same.
		if reservedBandwidth > 0 && (reservedBandwidth >= egressCapacity || reservedBandwidth >= ingressCapacity) {
			return nil, fmt.Errorf("invalid bandwidth reservation: %d on NIC: %s with capacity: [%d/%d]", reservedBandwidth, iface.Name, egressCapacity, ingressCapacity)
		}

		allocatableEgress := egressCapacity - reservedBandwidth
		allocatableIngress := ingressCapacity - reservedBandwidth

		defaultMachineState[iface.Name] = &NICState{
			EgressState: BandwidthInfo{
				Capacity:       egressCapacity,
				SysReservation: 0,
				Reservation:    reservedBandwidth,
				Allocatable:    allocatableEgress,
				Allocated:      0,
				Free:           allocatableEgress,
			},
			IngressState: BandwidthInfo{
				Capacity:       ingressCapacity,
				SysReservation: 0,
				Reservation:    reservedBandwidth,
				Allocatable:    allocatableIngress,
				Allocated:      0,
				Free:           allocatableIngress,
			},
			PodEntries: make(PodEntries),
		}
	}

	return defaultMachineState, nil
}

// GenerateMachineStateFromPodEntries returns NICMap for bandwidth based on
// machine info and reserved resources along with existed pod entries
func GenerateMachineStateFromPodEntries(conf *qrm.QRMPluginsConfiguration, nics []machine.InterfaceInfo,
	podEntries PodEntries, reservation map[string]uint32,
) (NICMap, error) {
	machineState, err := GenerateMachineState(conf, nics, reservation)
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineState failed with error: %v", err)
	}

	for nicName, nicState := range machineState {
		var allocatedEgressOnNIC, allocatedIngressOnNIC uint32 = 0, 0

		for podUID, containerEntries := range podEntries {
			for containerName, allocationInfo := range containerEntries {
				if containerName != "" && allocationInfo != nil && allocationInfo.IfName == nicName {
					allocatedEgressOnNIC += allocationInfo.Egress
					allocatedIngressOnNIC += allocationInfo.Ingress

					nicState.SetAllocationInfo(podUID, containerName, allocationInfo)
				}
			}
		}

		nicState.EgressState.Allocated = allocatedEgressOnNIC
		nicState.IngressState.Allocated = allocatedIngressOnNIC

		generalLog := general.LoggerWithPrefix("GenerateBandwidthStateFromPodEntries", general.LoggingPKGFull)
		if nicState.EgressState.Allocatable < nicState.EgressState.Allocated || nicState.IngressState.Allocatable < nicState.EgressState.Allocated {
			generalLog.Warningf("invalid allocated egress bandwidth: %d on NIC: %s"+
				" with allocatable bandwidth size: %d, total egress capacity size: %d, reserved bandwidth size: %d",
				nicState.EgressState.Allocated, nicName, nicState.EgressState.Allocatable, nicState.EgressState.Capacity, nicState.EgressState.Reservation)
			nicState.EgressState.Allocatable = nicState.EgressState.Allocated
		}
		nicState.EgressState.Free = nicState.EgressState.Allocatable - nicState.EgressState.Allocated

		if nicState.IngressState.Allocatable < nicState.IngressState.Allocated {
			generalLog.Warningf("invalid allocated ingress bandwidth: %d on NIC: %s"+
				" with allocatable bandwidth size: %d, total ingress capacity size: %d, reserved bandwidth size: %d",
				nicState.IngressState.Allocated, nicName, nicState.IngressState.Allocatable, nicState.IngressState.Capacity, nicState.EgressState.Reservation)
			nicState.IngressState.Allocatable = nicState.IngressState.Allocated
		}
		nicState.IngressState.Free = nicState.IngressState.Allocatable - nicState.IngressState.Allocated

		machineState[nicName] = nicState
	}

	return machineState, nil
}
