//go:build linux
// +build linux

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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

func TestGetExtraNetworkInfo(t *testing.T) {
	t.Parallel()

	conf := &global.MachineInfoConfiguration{
		NetMultipleNS:   false,
		NetNSDirAbsPath: "",
	}

	netInfo, err := GetExtraNetworkInfo(conf)
	assert.Nil(t, err)
	assert.NotNil(t, netInfo)
}

func TestGetInterfaceAttr(t *testing.T) {
	t.Parallel()

	nic := &InterfaceInfo{
		Iface: "eth0",
	}

	getInterfaceAttr(nic, "/sys/class/net")
	assert.Nil(t, nic)
	assert.Equal(t, -1, nic.NumaNode)
	assert.Equal(t, false, nic.Enable)
}
