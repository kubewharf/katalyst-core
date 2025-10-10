package machine

import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

type DeviceTopologyProvider interface {
	getDeviceTopology() (*DeviceTopology, bool, error)
	setDeviceTopology(*DeviceTopology) error
}

type DeviceTopologyRegistry struct {
	// DeviceNameToProvider is a mapping of device name to their respective topology provider
	DeviceNameToProvider map[string]DeviceTopologyProvider
}

func NewDeviceTopologyRegistry() *DeviceTopologyRegistry {
	return &DeviceTopologyRegistry{
		DeviceNameToProvider: make(map[string]DeviceTopologyProvider),
	}
}

// RegisterDeviceTopologyProvider registers a device topology provider for the specified device name.
func (r *DeviceTopologyRegistry) RegisterDeviceTopologyProvider(
	deviceName string, deviceTopologyProvider DeviceTopologyProvider,
) {
	r.DeviceNameToProvider[deviceName] = deviceTopologyProvider
}

// SetDeviceTopology sets the device topology for the specified device name.
func (r *DeviceTopologyRegistry) SetDeviceTopology(deviceName string, deviceTopology *DeviceTopology) error {
	provider, ok := r.DeviceNameToProvider[deviceName]
	if !ok {
		return fmt.Errorf("no device topology provider found for device %s", deviceName)
	}

	return provider.setDeviceTopology(deviceTopology)
}

// GetDeviceTopology gets the device topology for the specified device name.
func (r *DeviceTopologyRegistry) GetDeviceTopology(deviceName string) (*DeviceTopology, bool, error) {
	provider, ok := r.DeviceNameToProvider[deviceName]
	if !ok {
		return nil, false, fmt.Errorf("no device topology provider found for device %s", deviceName)
	}
	return provider.getDeviceTopology()
}

type DeviceTopology struct {
	Devices map[string]DeviceInfo
}

type DeviceInfo struct {
	Health    string
	NumaNodes []int
}

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

func (p *deviceTopologyProviderImpl) setDeviceTopology(deviceTopology *DeviceTopology) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if deviceTopology == nil {
		return fmt.Errorf("deviceTopology is nil")
	}

	p.deviceTopology = deviceTopology
	p.numaTopologyReady = checkDeviceNUMATopologyReady(deviceTopology)
	return nil
}

func (p *deviceTopologyProviderImpl) getDeviceTopology() (*DeviceTopology, bool, error) {
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
		gpuDevice, ok := registeredDevs[resourceName]
		if !ok {
			continue
		}

		for _, id := range gpuDevice {
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
