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
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
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

	// topologyChangeNotifiers is a list of callbacks to invoke when topology changes
	topologyChangeNotifiers []func()
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
		topologyAffinityProvider.SetDeviceAffinity(deviceTopology)
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
// It returns a map of device name to their respective device topology.
func (r *DeviceTopologyRegistry) GetDeviceTopologies(deviceNames []string) (map[string]*DeviceTopology, error) {
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
		return nil, fmt.Errorf("failed to get any device topology")
	}

	return topologies, nil
}

// GetLatestDeviceTopology gets device topologies for the given device names and picks the latest one.
func (r *DeviceTopologyRegistry) GetLatestDeviceTopology(deviceNames []string) (*DeviceTopology, error) {
	topologiesMap, err := r.GetDeviceTopologies(deviceNames)
	if err != nil {
		return nil, err
	}

	latestTopology := PickLatestDeviceTopology(topologiesMap)
	if latestTopology == nil {
		return nil, fmt.Errorf("no latest device topology")
	}

	return latestTopology, nil
}

// GetAffinityDevices returns, for each device id in deviceA, the set of deviceB ids
// grouped by affinity priority (and dimension) that share the same AffinityPriority key.
// The returned structure is:
//
//	map[deviceAId]DeviceAffinity, where DeviceAffinity is map[AffinityPriority]DeviceIDs.
//
// If no affinities exist for a deviceAId, that id is omitted from the result.
func (r *DeviceTopologyRegistry) GetAffinityDevices(deviceA, deviceB string) (map[string]DeviceAffinity, error) {
	deviceTopologyA, err := r.GetDeviceTopology(deviceA)
	if err != nil {
		return nil, fmt.Errorf("error getting device topology for device %s: %v", deviceA, err)
	}

	deviceTopologyB, err := r.GetDeviceTopology(deviceB)
	if err != nil {
		return nil, fmt.Errorf("error getting device topology for device %s: %v", deviceB, err)
	}

	result := make(map[string]DeviceAffinity)
	for deviceNameA, deviceInfoA := range deviceTopologyA.Devices {
		// Build DeviceAffinity grouped by concrete AffinityPriority (priority level + dimension)
		grouped := make(map[AffinityPriority]sets.String)
		for apA := range deviceInfoA.DeviceAffinity {
			for deviceNameB, deviceInfoB := range deviceTopologyB.Devices {
				if _, ok := deviceInfoB.DeviceAffinity[apA]; ok {
					if _, exists := grouped[apA]; !exists {
						grouped[apA] = sets.NewString()
					}
					grouped[apA].Insert(deviceNameB)
				}
			}
		}

		if len(grouped) > 0 {
			da := make(DeviceAffinity)
			for pri, setIDs := range grouped {
				// Materialize as a plain slice (unordered) to avoid biasing consumers
				da[pri] = setIDs.UnsortedList()
			}
			result[deviceNameA] = da
		}
	}

	return result, nil
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

// GetDeviceIDsByPriorityLevel returns the deviceIDs grouped by priority level (integer).
func (d DeviceAffinity) GetDeviceIDsByPriorityLevel() map[int]DeviceIDs {
	// Deduplicate device IDs per priority level since multiple AffinityPriority
	// entries can share the same PriorityLevel with overlapping device sets.
	byLevel := make(map[int]sets.String)
	for ap, ids := range d {
		lvl := ap.GetPriorityLevel()
		if byLevel[lvl] == nil {
			byLevel[lvl] = sets.NewString()
		}
		// Insert all IDs into the set to ensure uniqueness
		byLevel[lvl].Insert(ids...)
	}

	// Materialize into plain slices (order not guaranteed).
	res := make(map[int]DeviceIDs, len(byLevel))
	for lvl, setIDs := range byLevel {
		res[lvl] = setIDs.UnsortedList()
	}
	return res
}

type DeviceInfo struct {
	Health         string
	NumaNodes      []int
	DeviceAffinity DeviceAffinity
}

func (i DeviceInfo) GetDimensions() []Dimension {
	dimensions := make([]Dimension, 0)
	for priority := range i.DeviceAffinity {
		// filter out invalid dimensions because some invalid/empty configurations may be passed
		if priority.Dimension.Name == "" || priority.Dimension.Value == "" {
			continue
		}
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

// PickLatestDeviceTopology selects the latest device topology from the given map based on UpdateTime.
func PickLatestDeviceTopology(topologies map[string]*DeviceTopology) *DeviceTopology {
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
