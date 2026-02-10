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
)

// DeviceTopologyRegistry is a registry of all topology providers that knows how to provide topology information of machine devices
type DeviceTopologyRegistry struct {
	mux sync.RWMutex

	// deviceTopologyProviders is a mapping of device name to their respective topology provider
	deviceTopologyProviders map[string]DeviceTopologyProvider

	// deviceTopologyAffinityProviders is a mapping of device name to their respective affinity provider
	deviceTopologyAffinityProviders map[string]DeviceAffinityProvider

	// lastDeviceTopologies is a mapping of device name to their respective last device topology
	lastDeviceTopologies map[string]*DeviceTopology
}

func NewDeviceTopologyRegistry() *DeviceTopologyRegistry {
	return &DeviceTopologyRegistry{
		deviceTopologyProviders:         make(map[string]DeviceTopologyProvider),
		deviceTopologyAffinityProviders: make(map[string]DeviceAffinityProvider),
		lastDeviceTopologies:            make(map[string]*DeviceTopology),
	}
}

func (r *DeviceTopologyRegistry) Run(stopCh <-chan struct{}, initializedCh <-chan struct{}) {
	r.runAffinityProviders(stopCh, initializedCh)
}

// runAffinityProviders monitors all registered device affinity providers and applies topology updates when changes occur.
// It first waits until all the affinity providers are initialized.
// Then, it launches a goroutine per provider to watch for changes. When a provider reports a change, the corresponding
// DeviceTopology is updated via SetDeviceAffinity. The function blocks until stopCh is closed.
func (r *DeviceTopologyRegistry) runAffinityProviders(stopCh <-chan struct{}, initializedCh <-chan struct{}) {
	// wait until all affinity providers are initialized
	general.Infof("waiting for initializedCh...")
	select {
	case <-initializedCh:
		// proceed
	case <-stopCh:
		return
	}

	general.Infof("initialization done, starting watchers...")

	topologyChangedCh := make(chan string, len(r.deviceTopologyProviders))
	for deviceName, affinityProvider := range r.deviceTopologyAffinityProviders {
		general.Infof("watching topology change for %s", deviceName)
		go affinityProvider.WatchTopologyChanged(stopCh, topologyChangedCh, deviceName)
	}

	for {
		select {
		case <-stopCh:
			return
		case name := <-topologyChangedCh:
			general.Infof("topology change is detected for device %s", name)

			// Get the recent device topology and update it
			lastDeviceTopology, ok := r.lastDeviceTopologies[name]
			if !ok {
				general.Errorf("no last device topology for device %q when running affinity providers", name)
				continue
			}

			if err := r.SetDeviceTopology(name, lastDeviceTopology); err != nil {
				general.Errorf("failed to set new device topology for device %q: %v", name, err)
			}
		}
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

	// Cache the device topology
	r.lastDeviceTopologies[deviceName] = deviceTopology

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

// GetDeviceNUMAAffinity retrieves a map of a certain device A to the list of devices in device B that it has an affinity with.
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
			deviceKeyNUMANodes := keyInfo.GetNUMANodes()
			deviceValueNUMANodes := valueInfo.GetNUMANodes()

			if len(deviceKeyNUMANodes) != 0 && sets.NewInt(deviceKeyNUMANodes...).Equal(sets.NewInt(deviceValueNUMANodes...)) {
				devicesWithAffinity = append(devicesWithAffinity, valueName)
			}
		}
		deviceAffinity[keyName] = devicesWithAffinity
	}

	return deviceAffinity, nil
}

type DeviceTopology struct {
	Devices map[string]DeviceInfo
	// PriorityDimensions distinguishes the different dimensions of device affinity and their priority level.
	// The priority level is determined by the order of the dimensions in the slice.
	// For example, if devices have affinity based on the NUMA and SOCKET, and NUMA has higher priority than SOCKET,
	// the priority dimensions are ["NUMA", "SOCKET"].
	PriorityDimensions []string
}

// GroupDeviceAffinity forms a topology graph such that all devices within a DeviceIDs group have an affinity with each other.
// They are differentiated by their affinity priority value.
// E.g. Output:
//
//	{
//		0: {{"gpu-0", "gpu-1"}, {"gpu-2", "gpu-3"}},
//		1: {{"gpu-0", "gpu-1", "gpu-2", "gpu-3"}}
//	}
//
// means that gpu-0 and gpu-1 have an affinity with each other, gpu-2 and gpu-3 have an affinity with each other in affinity priority 0.
// and gpu-0, gpu-1, gpu-2, and gpu-3 have an affinity with each other in affinity priority 1.
func (t *DeviceTopology) GroupDeviceAffinity() map[int][]DeviceIDs {
	deviceAffinityGroup := make(map[int][]DeviceIDs)
	for deviceId, deviceInfo := range t.Devices {
		for priority, affinityDeviceIDs := range deviceInfo.DeviceAffinity {
			// Add itself in the group if it is not already included
			if !slices.Contains(affinityDeviceIDs, deviceId) {
				affinityDeviceIDs = append(affinityDeviceIDs, deviceId)
			}
			// Sort the strings for easier deduplication
			sort.Strings(affinityDeviceIDs)

			priorityLevel := priority.GetPriorityLevel()
			if _, ok := deviceAffinityGroup[priorityLevel]; !ok {
				deviceAffinityGroup[priorityLevel] = make([]DeviceIDs, 0)
			}

			// Add the affinityDeviceIDs to the priority level if it is not already there
			if !containsGroup(deviceAffinityGroup[priorityLevel], affinityDeviceIDs) {
				deviceAffinityGroup[priorityLevel] = append(deviceAffinityGroup[priorityLevel], affinityDeviceIDs)
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

// DeviceAffinity is the map of priority level to the other deviceIds that a particular deviceId has an affinity with
type DeviceAffinity map[AffinityPriority]DeviceIDs

type DeviceInfo struct {
	Health         string
	NumaNodes      []int
	DeviceAffinity DeviceAffinity
}

func (i DeviceInfo) GetDimensions() []Dimension {
	dimensions := make([]Dimension, 0)
	for priority := range i.DeviceAffinity {
		dimensions = append(dimensions, priority.Dimension)
	}

	sort.Slice(dimensions, func(i, j int) bool {
		return dimensions[i].Name < dimensions[j].Name
	})

	return dimensions
}

// AffinityPriority represents the level of affinity that a deviceID has with another deviceID.
// It contains the actual priority level and the dimension of the affinity.
// The priority level is the value of the priority. The lower the value, the higher the priority.
type AffinityPriority struct {
	PriorityLevel int
	Dimension     Dimension
}

func (a *AffinityPriority) GetPriorityLevel() int {
	return a.PriorityLevel
}

func (a *AffinityPriority) GetDimension() Dimension {
	return a.Dimension
}

// Dimension represents the dimension of the affinity.
// Name is the name of the dimension. E.g. NUMA, SOCKET, etc.
// Value is the id of the dimension. E.g. numa-0, numa-1, socket-0, socket-1, etc.
type Dimension struct {
	Name  string
	Value string
}

func (d *Dimension) GetName() string {
	return d.Name
}

func (d *Dimension) GetValue() string {
	return d.Value
}

type DeviceIDs []string

func (i DeviceInfo) GetNUMANodes() []int {
	if i.NumaNodes == nil {
		return []int{}
	}
	return i.NumaNodes
}

type DeviceTopologyProvider interface {
	GetDeviceTopology() (*DeviceTopology, bool, error)
	SetDeviceTopology(*DeviceTopology) error
}

type deviceTopologyProviderImpl struct {
	mutex         sync.RWMutex
	resourceNames []string

	deviceTopology    *DeviceTopology
	numaTopologyReady bool
}

var _ DeviceTopologyProvider = (*deviceTopologyProviderImpl)(nil)

func NewDeviceTopologyProvider(resourceNames []string) DeviceTopologyProvider {
	deviceTopology := &DeviceTopology{
		Devices: make(map[string]DeviceInfo),
	}

	return &deviceTopologyProviderImpl{
		deviceTopology: deviceTopology,
		resourceNames:  resourceNames,
	}
}

func (p *deviceTopologyProviderImpl) SetDeviceTopology(deviceTopology *DeviceTopology) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if deviceTopology == nil {
		return fmt.Errorf("deviceTopology is nil when setting device topology")
	}

	p.deviceTopology = deviceTopology
	p.numaTopologyReady = checkDeviceNUMATopologyReady(deviceTopology)
	return nil
}

func (p *deviceTopologyProviderImpl) GetDeviceTopology() (*DeviceTopology, bool, error) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	if p.deviceTopology == nil {
		return nil, false, fmt.Errorf("deviceTopology is nil when getting device topology")
	}

	return p.deviceTopology, p.numaTopologyReady, nil
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
