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

package checker

import (
	"errors"
	"net"
	"testing"

	"github.com/bytedance/mockey"
	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// Mock for net.InterfaceByName
var mockInterfaces = map[string]*net.Interface{
	"eth0": {Name: "eth0"},
	"eth1": {Name: "eth1"},
}

func mockInterfaceByName(name string) (*net.Interface, error) {
	if iface, exists := mockInterfaces[name]; exists {
		return iface, nil
	}
	return nil, errors.New("interface not found")
}

// Mock for machine.GetInterfaceAddr
var mockAddrs = map[string]*machine.IfaceAddr{
	"eth0": {
		IPV4: []*net.IP{
			parseIP("192.168.1.1"),
		},
		IPV6: []*net.IP{},
	},
	"eth1": {
		IPV4: []*net.IP{},
		IPV6: []*net.IP{},
	},
}

func parseIP(ip string) *net.IP {
	res := net.ParseIP(ip)
	return &res
}

func mockGetInterfaceAddr(iface net.Interface) (*machine.IfaceAddr, error) {
	if addr, exists := mockAddrs[iface.Name]; exists {
		return addr, nil
	}
	return nil, errors.New("failed to get address")
}

func TestIPChecker_CheckHealth(t *testing.T) {
	t.Parallel()

	mockey.Mock(net.InterfaceByName).To(mockInterfaceByName).Build()
	mockey.Mock(machine.GetInterfaceAddr).To(mockGetInterfaceAddr).Build()

	checker, err := NewIPChecker()
	assert.NoError(t, err)

	tests := []struct {
		name      string
		iface     string
		expected  bool
		expectErr bool
	}{
		{
			name:     "interface exists with IP",
			iface:    "eth0",
			expected: true,
		},
		{
			name:     "interface exists but no IP",
			iface:    "eth1",
			expected: false,
		},
		{
			name:      "interface does not exist",
			iface:     "eth2",
			expected:  false,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		info := machine.InterfaceInfo{Name: tt.iface}
		result, err := checker.CheckHealth(info)

		assert.Equal(t, tt.expected, result)
		if tt.expectErr {
			assert.Error(t, err)
		} else {
			assert.NoError(t, err)
		}
	}
}
