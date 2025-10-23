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

// DeviceAffinityProvider knows how to form affinity between devices
type DeviceAffinityProvider interface {
	// SetDeviceAffinity modifies DeviceTopology by retrieving each device's affinity to other devices
	SetDeviceAffinity(*DeviceTopology)
}

type DefaultDeviceAffinityProvider struct{}

var _ DeviceAffinityProvider = (*DefaultDeviceAffinityProvider)(nil)

func (p *DefaultDeviceAffinityProvider) SetDeviceAffinity(*DeviceTopology) {}

type GPUDeviceAffinityProvider struct {
	// isAffinityFunc is a function to check if 2 devices have affinity to each other
	isAffinityFunc func(deviceId1, deviceId2 string) bool
}

func NewGPUDeviceAffinityProvider(isAffinityFunc func(deviceId1, deviceId2 string) bool) DeviceAffinityProvider {
	return &GPUDeviceAffinityProvider{isAffinityFunc: isAffinityFunc}
}

// SetDeviceAffinity forms device affinity for the first layer only.
func (p *GPUDeviceAffinityProvider) SetDeviceAffinity(deviceTopology *DeviceTopology) {
	for device1, deviceInfo1 := range deviceTopology.Devices {
		if deviceInfo1.DeviceAffinityMap == nil {
			deviceInfo1.DeviceAffinityMap = make(map[AffinityPriority]DeviceIDs)
		}

		for device2, deviceInfo2 := range deviceTopology.Devices {
			if device1 == device2 {
				continue
			}

			if deviceInfo2.DeviceAffinityMap == nil {
				deviceInfo2.DeviceAffinityMap = make(map[AffinityPriority]DeviceIDs)
			}

			if p.isAffinityFunc(device1, device2) {
				if deviceInfo2.DeviceAffinityMap[0] == nil {
					deviceInfo2.DeviceAffinityMap[0] = make(DeviceIDs, 0)
				}
				deviceInfo1.DeviceAffinityMap[0] = append(deviceInfo1.DeviceAffinityMap[0], device2)

				if deviceInfo1.DeviceAffinityMap[0] == nil {
					deviceInfo1.DeviceAffinityMap[0] = make(DeviceIDs, 0)
				}
				deviceInfo2.DeviceAffinityMap[0] = append(deviceInfo2.DeviceAffinityMap[0], device1)
			}
		}
	}
}
