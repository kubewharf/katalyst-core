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
	"reflect"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	resyncInterval = 30 * time.Second
	DimensionNuma  = "numa"
	// FallbackNUMANodeID is used when a device has no reported NUMA topology.
	FallbackNUMANodeID = 0
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

	// topologyChangeNotifiers is a list of callbacks to invoke when topology changes
	topologyChangeNotifiers []func()

	// emitter is used to emit metrics
	emitter metrics.MetricEmitter
}

func NewDeviceTopologyRegistry(emitter metrics.MetricEmitter) *DeviceTopologyRegistry {
	if emitter == nil {
		emitter = metrics.DummyMetrics{}
	}
	return &DeviceTopologyRegistry{
		deviceTopologyProviders:         make(map[string]DeviceTopologyProvider),
		deviceTopologyAffinityProviders: make(map[string]DeviceAffinityProvider),
		lastDeviceTopologies:            make(map[string]*DeviceTopology),
		emitter:                         emitter,
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

// RegisterTopologyChangeNotifier registers a callback that will be invoked whenever any device topology actually changes.
func (r *DeviceTopologyRegistry) RegisterTopologyChangeNotifier(notifier func()) {
	r.mux.Lock()
	defer r.mux.Unlock()
	r.topologyChangeNotifiers = append(r.topologyChangeNotifiers, notifier)
}

// SetDeviceTopology sets the device topology for the specified device name.
func (r *DeviceTopologyRegistry) SetDeviceTopology(deviceName string, deviceTopology *DeviceTopology) error {
	r.mux.Lock()

	topologyProvider, ok := r.deviceTopologyProviders[deviceName]
	if !ok {
		r.mux.Unlock()
		return fmt.Errorf("no device topology provider found for device %s", deviceName)
	}

	topologyAffinityProvider, ok := r.deviceTopologyAffinityProviders[deviceName]
	if ok {
		generateAndSetDeviceAffinity(topologyAffinityProvider, deviceTopology, r.emitter)
		general.Infof("set device affinity provider for device %s, %v", deviceName, deviceTopology)
	} else {
		general.Infof("no device affinity provider found for device %s", deviceName)
	}

	// Capture notifiers to invoke outside the lock to avoid deadlocks
	var notifiers []func()
	err := topologyProvider.SetDeviceTopology(deviceTopology)
	if err != nil {
		general.Errorf("failed to set device topology for device %s, err: %v, skip triggering notifiers", deviceName, err)
	} else {
		// Check if topology has actually changed (DeviceTopology is small, so reflect.DeepEqual is fast)
		changed := !reflect.DeepEqual(r.lastDeviceTopologies[deviceName], deviceTopology)

		// Cache the device topology only when SetDeviceTopology succeeds
		r.lastDeviceTopologies[deviceName] = deviceTopology

		if changed {
			general.Infof("device topology changed for device %s, triggering %d notifiers", deviceName, len(r.topologyChangeNotifiers))
			notifiers = append(notifiers, r.topologyChangeNotifiers...)
		} else {
			general.Infof("device topology unchanged for device %s, skip triggering notifiers", deviceName)
		}
	}
	r.mux.Unlock()

	// Invoke notifiers outside the lock
	for _, notifier := range notifiers {
		notifier()
	}

	return err
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

// GetDeviceTopologies gets device topologies for the given device names.
// It returns a map of device name to their respective device topology,
// along with a boolean indicating whether any topology is found.
func (r *DeviceTopologyRegistry) GetDeviceTopologies(deviceNames []string) (map[string]*DeviceTopology, bool) {
	r.mux.RLock()
	defer r.mux.RUnlock()

	topologies := make(map[string]*DeviceTopology)
	for _, deviceName := range deviceNames {
		topology, err := r.getDeviceTopology(deviceName)
		if err != nil {
			general.Warningf("failed to get topology for device %s: %v", deviceName, err)
			continue
		}
		topologies[deviceName] = topology
	}

	if len(topologies) == 0 {
		return nil, false
	}

	return topologies, true
}

// GetLatestDeviceTopology gets device topologies for the given device names and picks the latest one.
// It also returns the device name whose topology was picked.
func (r *DeviceTopologyRegistry) GetLatestDeviceTopology(deviceNames []string) (*DeviceTopology, string, error) {
	topologiesMap, ok := r.GetDeviceTopologies(deviceNames)
	if !ok {
		return nil, "", fmt.Errorf("failed to get any device topology")
	}

	latestTopology, latestName := PickLatestDeviceTopology(topologiesMap)
	if latestTopology == nil {
		return nil, "", fmt.Errorf("no latest device topology")
	}

	return latestTopology, latestName, nil
}

// getAffinityFromDimensions returns, for each device id in deviceTopologyA.Devices,
// the set of device ids in deviceTopologyB.Devices grouped by dimension key,
// such that the dimension value is the same between deviceA and deviceB.
func getAffinityFromDimensions(deviceTopologyA, deviceTopologyB *DeviceTopology) map[string]map[string]DeviceIDs {
	result := make(map[string]map[string]DeviceIDs)
	for deviceNameA, deviceInfoA := range deviceTopologyA.Devices {
		// Group deviceB by dimension key
		grouped := make(map[string]sets.String)
		// Iterate over each dimension of deviceA
		for dimName, dimValueA := range deviceInfoA.Dimensions {
			// Find all deviceB with the same dimension value
			for deviceNameB, deviceInfoB := range deviceTopologyB.Devices {
				dimValueB, ok := deviceInfoB.Dimensions[dimName]
				if !ok {
					continue
				}
				if dimValueB != dimValueA {
					continue
				}
				if _, exists := grouped[dimName]; !exists {
					grouped[dimName] = sets.NewString()
				}
				grouped[dimName].Insert(deviceNameB)
			}
		}

		if len(grouped) > 0 {
			dimensionGroups := make(map[string]DeviceIDs)
			for dimName, setIDs := range grouped {
				dimensionGroups[dimName] = setIDs.UnsortedList()
			}
			result[deviceNameA] = dimensionGroups
		}
	}
	return result
}

// getAffinityFromNumaNodes returns, for each device id in deviceTopologyA.Devices,
// the set of device ids in deviceTopologyB.Devices grouped under "numa" key,
// such that they share at least one NUMA node with deviceA.
func getAffinityFromNumaNodes(deviceTopologyA, deviceTopologyB *DeviceTopology) map[string]map[string]DeviceIDs {
	result := make(map[string]map[string]DeviceIDs)
	for deviceNameA, deviceInfoA := range deviceTopologyA.Devices {
		if len(deviceInfoA.NumaNodes) == 0 {
			continue
		}

		// Use a set to track unique NUMA node values for deviceA
		numaSetA := sets.NewInt(deviceInfoA.NumaNodes...)
		grouped := make(map[string]sets.String)
		for deviceNameB, deviceInfoB := range deviceTopologyB.Devices {
			for _, numaNodeB := range deviceInfoB.NumaNodes {
				if numaSetA.Has(numaNodeB) {
					if _, exists := grouped[DimensionNuma]; !exists {
						grouped[DimensionNuma] = sets.NewString()
					}
					grouped[DimensionNuma].Insert(deviceNameB)
					break // no need to check other numa nodes for this deviceB
				}
			}
		}

		if len(grouped) > 0 {
			dimensionGroups := make(map[string]DeviceIDs)
			for dimName, setIDs := range grouped {
				dimensionGroups[dimName] = setIDs.UnsortedList()
			}
			result[deviceNameA] = dimensionGroups
		}
	}
	return result
}

// GetAffinityDevices returns, for each device id in deviceA, the set of deviceB ids
// grouped by dimension key, first checking Dimensions, then falling back to NumaNodes.
// The returned structure is:
//
//	map[deviceAId]map[dimensionKey]DeviceIDs.
//
// If no affinities exist for a deviceAId, that id is omitted from the result.
func (r *DeviceTopologyRegistry) GetAffinityDevices(deviceA, deviceB string) (map[string]map[string]DeviceIDs, error) {
	deviceTopologyA, err := r.GetDeviceTopology(deviceA)
	if err != nil {
		return nil, fmt.Errorf("error getting device topology for device %s: %v", deviceA, err)
	}

	deviceTopologyB, err := r.GetDeviceTopology(deviceB)
	if err != nil {
		return nil, fmt.Errorf("error getting device topology for device %s: %v", deviceB, err)
	}

	result := getAffinityFromDimensions(deviceTopologyA, deviceTopologyB)
	if len(result) == 0 {
		result = getAffinityFromNumaNodes(deviceTopologyA, deviceTopologyB)
	}

	return result, nil
}

// HasAnyDeviceAffinity returns true if there exists at least one (a, b) pair
// where a is in deviceASet and b is in deviceBSet such that affinity exists
// between them. It short-circuits on the first matching dimension or NUMA-node
// pair instead of materializing the full affinity map.
func (r *DeviceTopologyRegistry) HasAnyDeviceAffinity(deviceASet, deviceBSet []string) bool {
	for _, deviceA := range deviceASet {
		topoA, err := r.GetDeviceTopology(deviceA)
		if err != nil {
			continue
		}
		for _, deviceB := range deviceBSet {
			topoB, err := r.GetDeviceTopology(deviceB)
			if err != nil {
				continue
			}
			if hasAnyDeviceAffinity(topoA, topoB) {
				general.Infof("found affinity between device %s and device %s", deviceA, deviceB)
				return true
			}
		}
	}
	return false
}

// hasAnyDeviceAffinity reports whether any device in topoA shares a dimension
// value or a NUMA node with any device in topoB. It returns on the first hit
// without building the full affinity map.
func hasAnyDeviceAffinity(topoA, topoB *DeviceTopology) bool {
	for _, devA := range topoA.Devices {
		for _, devB := range topoB.Devices {
			for dim, valA := range devA.Dimensions {
				if valB, ok := devB.Dimensions[dim]; ok && valA == valB {
					return true
				}
			}
			for _, numaA := range devA.NumaNodes {
				for _, numaB := range devB.NumaNodes {
					if numaA == numaB {
						return true
					}
				}
			}
		}
	}
	return false
}

type DeviceTopology struct {
	Devices map[string]DeviceInfo
	// PriorityDimensions distinguishes the different dimensions of device affinity and their priority level.
	// The priority level is determined by the order of the dimensions in the slice.
	// For example, if devices have affinity based on the NUMA and SOCKET, and NUMA has higher priority than SOCKET,
	// the priority dimensions are ["NUMA", "SOCKET"].
	PriorityDimensions []string
	// UpdateTime is the timestamp when the topology was last updated.
	UpdateTime int64
}

func (t *DeviceTopology) IsDeviceHealthy(id string) (bool, bool) {
	deviceInfo, ok := t.Devices[id]
	if !ok {
		return false, false
	}
	return deviceInfo.Health == pluginapi.Healthy, true
}

// GetDeviceNUMANodes returns the NUMA nodes associated with the given devices,
// falling back to FallbackNUMANodeID when a device has no NUMA topology.
func (t *DeviceTopology) GetDeviceNUMANodes(deviceIDs ...string) CPUSet {
	numaNodes := NewCPUSet()
	if t == nil || len(deviceIDs) == 0 {
		return numaNodes
	}

	for _, deviceID := range deviceIDs {
		device, ok := t.Devices[deviceID]
		if !ok {
			general.Errorf("device %q not found in topology", deviceID)
			return NewCPUSet()
		}
		if len(device.NumaNodes) == 0 {
			general.Warningf("device %q has no NUMA nodes; defaulting to NUMA %d", deviceID, FallbackNUMANodeID)
			numaNodes.Add(FallbackNUMANodeID)
			continue
		}
		numaNodes.Add(device.NumaNodes...)
	}
	return numaNodes
}

// GroupDeviceAffinity forms a topology graph such that all devices within a DeviceIDs group have an affinity with each other.
// The outer slice is ordered from the highest priority to the lowest priority.
// E.g. Output:
//
//	[
//		{{"gpu-0", "gpu-1"}, {"gpu-2", "gpu-3"}},
//		{{"gpu-0", "gpu-1", "gpu-2", "gpu-3"}},
//	]
//
// means that gpu-0 and gpu-1 have an affinity with each other, gpu-2 and gpu-3 have an affinity with each other in the highest affinity priority.
// and gpu-0, gpu-1, gpu-2, and gpu-3 have an affinity with each other in the next lower affinity priority.
func (t *DeviceTopology) GroupDeviceAffinity() [][]DeviceIDs {
	if t == nil || len(t.Devices) == 0 || len(t.PriorityDimensions) == 0 {
		return nil
	}

	priorityDimensionGroups := make([][]DeviceIDs, 0, len(t.PriorityDimensions))
	for _, name := range t.PriorityDimensions {
		// devicesGroup is a mapping of dimension value to the device IDs
		devicesGroup := make(map[string]sets.String)

		// Get all the devices of the same dimension value
		for id, info := range t.Devices {
			if len(info.Dimensions) == 0 {
				continue
			}

			value, ok := info.Dimensions[name]
			if !ok {
				continue
			}

			if _, ok = devicesGroup[value]; !ok {
				devicesGroup[value] = sets.NewString()
			}

			devicesGroup[value] = devicesGroup[value].Insert(id)
		}

		// If there are no devices in a certain group, do not add them in the priorityDimensionGroups
		if len(devicesGroup) == 0 {
			continue
		}

		priorityDevicesGroup := make([]DeviceIDs, 0, len(devicesGroup))
		// Iterate through all the devices and group them based on their value
		for _, ids := range devicesGroup {
			priorityDevicesGroup = append(priorityDevicesGroup, ids.UnsortedList())
		}

		priorityDimensionGroups = append(priorityDimensionGroups, priorityDevicesGroup)
	}

	return priorityDimensionGroups
}

// DeviceDimensions stores per-device dimension attributes, keyed by canonicalized dimension name.
// Example: {"numa": "0", "socket": "1"}.
// The key of the DeviceDimensions should be mapped to one of the PriorityDimensions
type DeviceDimensions map[string]string

type DeviceInfo struct {
	Health     string
	NumaNodes  []int
	Dimensions DeviceDimensions
}

func (i DeviceInfo) GetDimensions() DeviceDimensions {
	return i.Dimensions
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

// PickLatestDeviceTopology selects the latest device topology from the given map based on UpdateTime.
// It also returns the map key of the picked topology (empty string if none was picked).
func PickLatestDeviceTopology(topologies map[string]*DeviceTopology) (*DeviceTopology, string) {
	var latest *DeviceTopology
	var latestName string
	for name, t := range topologies {
		if t == nil {
			continue
		}

		if latest == nil || t.UpdateTime > latest.UpdateTime {
			latest = t
			latestName = name
		}
	}

	return latest, latestName
}
