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

import "sync"

type deviceTopologyProviderStub struct {
	mutex          sync.RWMutex
	deviceTopology *DeviceTopology
}

func NewDeviceTopologyProviderStub() DeviceTopologyProvider {
	return &deviceTopologyProviderStub{}
}

func (d *deviceTopologyProviderStub) SetDeviceTopology(deviceTopology *DeviceTopology) error {
	d.mutex.Lock()
	defer d.mutex.Unlock()

	d.deviceTopology = deviceTopology
	return nil
}

func (d *deviceTopologyProviderStub) GetDeviceTopology() (*DeviceTopology, bool, error) {
	d.mutex.RLock()
	defer d.mutex.RUnlock()

	return d.deviceTopology, true, nil
}

type deviceAffinityProviderStub struct {
	mutex     sync.Mutex
	setCalled bool
	// changeCh is a channel that mocks the behavior of detecting
	changeCh chan struct{}
}

func newAffinityProviderStub() DeviceAffinityProvider {
	return &deviceAffinityProviderStub{
		changeCh: make(chan struct{}, 1),
	}
}

func (d *deviceAffinityProviderStub) SetDeviceAffinity(topology *DeviceTopology) {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	d.setCalled = true
}

func (d *deviceAffinityProviderStub) WatchTopologyChanged(stopCh <-chan struct{}) chan struct{} {
	changeCh := make(chan struct{}, 1)

	go func() {
		defer close(changeCh)
		for {
			select {
			case <-stopCh:
				return
			case <-d.changeCh:
				// Non-blocking send to avoid goroutine leak if nobody is reading
				select {
				case changeCh <- struct{}{}:
				default:
				}
			}
		}
	}()

	return changeCh
}

func (d *deviceAffinityProviderStub) TriggerChange() {
	d.changeCh <- struct{}{}
}

func (d *deviceAffinityProviderStub) WasSetCalled() bool {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.setCalled
}
