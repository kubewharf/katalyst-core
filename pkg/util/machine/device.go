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
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/strings/slices"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	resyncInterval = 30 * time.Second
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

func (r *DeviceTopologyRegistry) Run(stopCh <-chan struct{}) {
	r.runAffinityProviders(stopCh)
}

// runAffinityProviders launches a watcher goroutine for each device affinity provider.
// Each watcher listens for topology changes and updates the corresponding DeviceTopology.
// The function blocks until stopCh is closed, and gracefully handles providers that never emit events.
func (r *DeviceTopologyRegistry) runAffinityProviders(stopCh <-chan struct{}) {
	topologyChangedCh := make(chan string, len(r.deviceTopologyAffinityProviders))

	// For every affinity provider, start a goroutine to listen to topology changes
	for deviceName, affinityProvider := range r.deviceTopologyAffinityProviders {
		go func(name string, p DeviceAffinityProvider) {
			general.Infof("watching topology change for %s", name)
			ch := p.WatchTopologyChanged(stopCh)
			if ch == nil {
				return
			}

			for {
				select {
				case <-stopCh:
					return
				case <-ch:
					select { // non-blocking send
					case topologyChangedCh <- name:
					default:
						general.Infof("topology change dropped for device %s", name)
					}
				}
			}
		}(deviceName, affinityProvider)
	}

	ticker := time.NewTicker(resyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case name := <-topologyChangedCh:
			general.Infof("topology change is detected for device %s", name)
			r.updateTopology(name)
		case <-ticker.C:
			// Periodically resync all devices in case change events were dropped
			for name := range r.deviceTopologyAffinityProviders {
				r.updateTopology(name)
			}
		}
	}
}

func (r *DeviceTopologyRegistry) getLastTopology(deviceName string) (*DeviceTopology, error) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	lastDeviceTopology, ok := r.lastDeviceTopologies[deviceName]
	if !ok {
		return nil, fmt.Errorf("no last device topology for device %q", deviceName)
	}

	return lastDeviceTopology, nil
}

// updateTopology retrieves current cached topology for the device and updates the topology
func (r *DeviceTopologyRegistry) updateTopology(deviceName string) {
	// Get the recent device topology and update it
	lastDeviceTopology, err := r.getLastTopology(deviceName)
	if err != nil {
		general.Errorf("no last device topology for device %q when updating topology", deviceName)
		return
	}

	if err = r.SetDeviceTopology(deviceName, lastDeviceTopology); err != nil {
		general.Errorf("failed to set new device topology for device %q: %v", deviceName, err)
	}

	general.Infof("successfully updated device topology for device %q", deviceName)
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

func (r *DeviceTopologyRegistry) getDeviceTopology(deviceName string) (*DeviceTopology, error) {
	provider, ok := r.deviceTopologyProviders[deviceName]
	if !ok {
		return nil, fmt.Errorf("no device topology provider found for device %s", deviceName)
	}
	return provider.GetDeviceTopology()
}

// GetDeviceTopology gets the device topology for the specified device name.
func (r *DeviceTopologyRegistry) GetDeviceTopology(deviceName string) (*DeviceTopology, error) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	return r.getDeviceTopology(deviceName)
}

// GetLatestDeviceTopology gets device topologies for the given device names and picks the latest one.
func (r *DeviceTopologyRegistry) GetLatestDeviceTopology(deviceNames []string) (*DeviceTopology, error) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	var topologies []*DeviceTopology
	for _, deviceName := range deviceNames {
		topology, err := r.getDeviceTopology(deviceName)
		if err != nil {
			general.Warningf("failed to get topology for device %s: %v", deviceName, err)
			continue
		}
		topologies = append(topologies, topology)
	}

	if len(topologies) == 0 {
		return nil, fmt.Errorf("failed to get any device topology")
	}

	return pickLatestDeviceTopology(topologies...), nil
}

// GetDeviceNUMAAffinity retrieves a map of a certain device A to the list of devices in device B that it has an affinity with.
// A device is considered to have an affinity with another device if they are on the exact same NUMA node(s)
func (r *DeviceTopologyRegistry) GetDeviceNUMAAffinity(deviceA, deviceB string) (map[string][]string, error) {
	deviceTopologyKey, err := r.GetDeviceTopology(deviceA)
	if err != nil {
		return nil, fmt.Errorf("error getting device topology for device %s: %v", deviceA, err)
	}

	deviceTopologyValue, err := r.GetDeviceTopology(deviceB)
	if err != nil {
		return nil, fmt.Errorf("error getting device topology for device %s: %v", deviceB, err)
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
	// DeviceName is the name of the device, e.g. "nvidia.com/gpu"
	DeviceName string
	Devices    map[string]DeviceInfo
	// PriorityDimensions distinguishes the different dimensions of device affinity and their priority level.
	// The priority level is determined by the order of the dimensions in the slice.
	// For example, if devices have affinity based on the NUMA and SOCKET, and NUMA has higher priority than SOCKET,
	// the priority dimensions are ["NUMA", "SOCKET"].
	PriorityDimensions []string
	// UpdateTime is the timestamp when the topology was last updated.
	UpdateTime int64
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
	GetDeviceTopology() (*DeviceTopology, error)
	SetDeviceTopology(*DeviceTopology) error
}

type deviceTopologyProviderImpl struct {
	mutex sync.RWMutex

	deviceTopology *DeviceTopology
}

var _ DeviceTopologyProvider = (*deviceTopologyProviderImpl)(nil)

func NewDeviceTopologyProvider() DeviceTopologyProvider {
	return &deviceTopologyProviderImpl{}
}

func (p *deviceTopologyProviderImpl) SetDeviceTopology(deviceTopology *DeviceTopology) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if deviceTopology == nil {
		return fmt.Errorf("deviceTopology is nil when setting device topology")
	}

	p.deviceTopology = deviceTopology
	return nil
}

func (p *deviceTopologyProviderImpl) GetDeviceTopology() (*DeviceTopology, error) {
	p.mutex.RLock()
	defer p.mutex.RUnlock()

	if p.deviceTopology == nil {
		return nil, fmt.Errorf("deviceTopology is not initialized by SetDeviceTopology")
	}

	return p.deviceTopology, nil
}

// pickLatestDeviceTopology selects the latest device topology from the given list based on UpdateTime.
func pickLatestDeviceTopology(topologies ...*DeviceTopology) *DeviceTopology {
	var latest *DeviceTopology
	for _, t := range topologies {
		if t == nil {
			continue
		}

		if latest == nil || t.UpdateTime > latest.UpdateTime {
			latest = t
		}
	}

	return latest
}
