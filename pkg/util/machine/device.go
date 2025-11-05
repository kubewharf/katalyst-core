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

package machine

import (
	"fmt"
	"sort"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/strings/slices"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type DeviceTopologyProvider interface {
	GetDeviceTopology() (*DeviceTopology, bool, error)
	SetDeviceTopology(*DeviceTopology) error
}

// DeviceTopologyRegistry is a registry of all topology providers that knows how to provide topology information of machine devices
type DeviceTopologyRegistry struct {
	mux sync.RWMutex

	// deviceTopologyProviders is a mapping of device name to their respective topology provider
	deviceTopologyProviders map[string]DeviceTopologyProvider

	// deviceTopologyAffinityProviders is a mapping of device name to their respective affinity provider
	deviceTopologyAffinityProviders map[string]DeviceAffinityProvider
}

func NewDeviceTopologyRegistry() *DeviceTopologyRegistry {
	return &DeviceTopologyRegistry{
		deviceTopologyProviders:         make(map[string]DeviceTopologyProvider),
		deviceTopologyAffinityProviders: make(map[string]DeviceAffinityProvider),
	}
}

// RegisterDeviceTopologyProvider registers a device topology provider for the specified device name.
func (r *DeviceTopologyRegistry) RegisterDeviceTopologyProvider(
	deviceName string, deviceTopologyProvider DeviceTopologyProvider,
) {
	r.mux.Lock()
	defer r.mux.Unlock()

	r.deviceTopologyProviders[deviceName] = deviceTopologyProvider
}

func (r *DeviceTopologyRegistry) RegisterTopologyAffinityProvider(
	deviceName string, deviceAffinityProvider DeviceAffinityProvider,
) {
	r.mux.Lock()
	defer r.mux.Unlock()

	r.deviceTopologyAffinityProviders[deviceName] = deviceAffinityProvider
}

// SetDeviceTopology sets the device topology for the specified device name.
func (r *DeviceTopologyRegistry) SetDeviceTopology(deviceName string, deviceTopology *DeviceTopology) error {
	r.mux.Lock()
	defer r.mux.Unlock()

	topologyProvider, ok := r.deviceTopologyProviders[deviceName]
	if !ok {
		return fmt.Errorf("no device topology provider found for device %s", deviceName)
	}

	topologyAffinityProvider, ok := r.deviceTopologyAffinityProviders[deviceName]
	if ok {
		topologyAffinityProvider.SetDeviceAffinity(deviceTopology)
		general.Infof("set device affinity provider for device %s, %v", deviceName, deviceTopology)
	} else {
		general.Infof("no device affinity provider found for device %s", deviceName)
	}

	return topologyProvider.SetDeviceTopology(deviceTopology)
}

// GetAllDeviceTopologyProviders returns all registered device topology providers.
func (r *DeviceTopologyRegistry) GetAllDeviceTopologyProviders() map[string]DeviceTopologyProvider {
	r.mux.RLock()
	defer r.mux.RUnlock()

	return r.deviceTopologyProviders
}

// GetDeviceTopology gets the device topology for the specified device name.
func (r *DeviceTopologyRegistry) GetDeviceTopology(deviceName string) (*DeviceTopology, bool, error) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	provider, ok := r.deviceTopologyProviders[deviceName]
	if !ok {
		return nil, false, fmt.Errorf("no device topology provider found for device %s", deviceName)
	}
	return provider.GetDeviceTopology()
}

// GetDeviceNUMAAffinity retrieves a map of a certain device to the list of devices that it has an affinity with.
// A device is considered to have an affinity with another device if they are on the exact same NUMA node(s)
func (r *DeviceTopologyRegistry) GetDeviceNUMAAffinity(deviceA, deviceB string) (map[string][]string, error) {
	deviceTopologyKey, numaReady, err := r.GetDeviceTopology(deviceA)
	if err != nil {
		return nil, fmt.Errorf("error getting device topology for device %s: %v", deviceA, err)
	}
	if !numaReady {
		return nil, fmt.Errorf("device topology for device %s is not ready", deviceA)
	}

	deviceTopologyValue, numaReady, err := r.GetDeviceTopology(deviceB)
	if err != nil {
		return nil, fmt.Errorf("error getting device topology for device %s: %v", deviceB, err)
	}
	if !numaReady {
		return nil, fmt.Errorf("device topology for device %s is not ready", deviceB)
	}

	deviceAffinity := make(map[string][]string)
	for keyName, keyInfo := range deviceTopologyKey.Devices {
		devicesWithAffinity := make([]string, 0)
		for valueName, valueInfo := range deviceTopologyValue.Devices {
			if sets.NewInt(keyInfo.GetNUMANode()...).Equal(sets.NewInt(valueInfo.GetNUMANode()...)) {
				devicesWithAffinity = append(devicesWithAffinity, valueName)
			}
		}
		deviceAffinity[keyName] = devicesWithAffinity
	}

	return deviceAffinity, nil
}

type DeviceTopology struct {
	Devices map[string]DeviceInfo
}

// GroupDeviceAffinity forms a topology graph such that all groups of DeviceIDs within a certain affinity priority level
func (t *DeviceTopology) GroupDeviceAffinity() map[AffinityPriority][]DeviceIDs {
	deviceAffinityGroup := make(map[AffinityPriority][]DeviceIDs)
	for deviceId, deviceInfo := range t.Devices {
		for priority, affinityDeviceIDs := range deviceInfo.DeviceAffinity {
			// Add itself in the group if it is not already included
			if !slices.Contains(affinityDeviceIDs, deviceId) {
				affinityDeviceIDs = append(affinityDeviceIDs, deviceId)
			}
			// Sort the strings for easier deduplication
			sort.Strings(affinityDeviceIDs)
			if _, ok := deviceAffinityGroup[priority]; !ok {
				deviceAffinityGroup[priority] = make([]DeviceIDs, 0)
			}

			// Add the affinityDeviceIDs to the priority level if it is not already there
			if !containsGroup(deviceAffinityGroup[priority], affinityDeviceIDs) {
				deviceAffinityGroup[priority] = append(deviceAffinityGroup[priority], affinityDeviceIDs)
			}

		}
	}
	return deviceAffinityGroup
}

func containsGroup(groups []DeviceIDs, candidate DeviceIDs) bool {
	for _, g := range groups {
		if slices.Equal(g, candidate) {
			return true
		}
	}
	return false
}

func (t *DeviceTopology) GetDeviceAffinityMap(deviceId string) (map[AffinityPriority]DeviceIDs, error) {
	info, ok := t.Devices[deviceId]
	if !ok {
		return nil, fmt.Errorf("failed to find device %s in device topology", deviceId)
	}
	return info.DeviceAffinity, nil
}

type DeviceInfo struct {
	Health    string
	NumaNodes []int
	// DeviceAffinity is the map of priority level to the other deviceIds that a particular deviceId has an affinity with
	DeviceAffinity map[AffinityPriority]DeviceIDs
}

// AffinityPriority is the level of affinity that a deviceId has with another deviceId.
// The lowest affinityPriority value is 0, and in this level, devices have the most affinity with one another,
// so it is of highest priority to try to allocate these devices together.
// As the affinityPriority value increases, devices do not have as much affinity with each other,
// so it is of lower priority to try to allocate these devices together.
type AffinityPriority int

type DeviceIDs []string

func (i DeviceInfo) GetNUMANode() []int {
	if i.NumaNodes == nil {
		return []int{}
	}
	return i.NumaNodes
}

type deviceTopologyProviderImpl struct {
	mutex         sync.RWMutex
	resourceNames []string

	deviceTopology    *DeviceTopology
	numaTopologyReady bool
}

func NewDeviceTopologyProvider(resourceNames []string) DeviceTopologyProvider {
	deviceTopology, err := initDeviceTopology(resourceNames)
	if err != nil {
		deviceTopology = getEmptyDeviceTopology()
		general.Warningf("initDeviceTopology failed with error: %v", err)
	} else {
		general.Infof("initDeviceTopology success: %v", deviceTopology)
	}

	return &deviceTopologyProviderImpl{
		resourceNames:  resourceNames,
		deviceTopology: deviceTopology,
	}
}

func (p *deviceTopologyProviderImpl) SetDeviceTopology(deviceTopology *DeviceTopology) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if deviceTopology == nil {
		return fmt.Errorf("deviceTopology is nil")
	}

	p.deviceTopology = deviceTopology
	p.numaTopologyReady = checkDeviceNUMATopologyReady(deviceTopology)
	return nil
}

func (p *deviceTopologyProviderImpl) GetDeviceTopology() (*DeviceTopology, bool, error) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	return p.deviceTopology, p.numaTopologyReady, nil
}

func getEmptyDeviceTopology() *DeviceTopology {
	return &DeviceTopology{
		Devices: make(map[string]DeviceInfo),
	}
}

func initDeviceTopology(resourceNames []string) (*DeviceTopology, error) {
	deviceTopology := getEmptyDeviceTopology()

	kubeletCheckpoint, err := native.GetKubeletCheckpoint()
	if err != nil {
		general.Errorf("Failed to get kubelet checkpoint: %v", err)
		return deviceTopology, nil
	}

	_, registeredDevs := kubeletCheckpoint.GetDataInLatestFormat()
	for _, resourceName := range resourceNames {
		devices, ok := registeredDevs[resourceName]
		if !ok {
			continue
		}

		for _, id := range devices {
			// get NUMA node from UpdateAllocatableAssociatedDevices
			deviceTopology.Devices[id] = DeviceInfo{}
		}
	}
	return deviceTopology, nil
}

func checkDeviceNUMATopologyReady(topology *DeviceTopology) bool {
	if topology == nil {
		return false
	}

	for _, device := range topology.Devices {
		if device.NumaNodes == nil {
			return false
		}
	}
	return true
}
