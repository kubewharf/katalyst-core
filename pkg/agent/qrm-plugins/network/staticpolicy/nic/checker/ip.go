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
	"net"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	HealthCheckerNameIP = "ip"
)

type ipChecker struct{}

func NewIPChecker() (NICHealthChecker, error) {
	return &ipChecker{}, nil
}

func (c *ipChecker) CheckHealth(info machine.InterfaceInfo) (bool, error) {
	iface, err := net.InterfaceByName(info.Name)
	if err != nil {
		return false, err
	}

	if iface == nil {
		general.Errorf("IP checker for interface %s not found", info.Name)
		return false, nil
	}

	addr, err := machine.GetInterfaceAddr(*iface)
	if err != nil {
		return false, err
	}

	if len(addr.GetNICIPs(machine.IPVersionV4)) == 0 && len(addr.GetNICIPs(machine.IPVersionV6)) == 0 {
		return false, nil
	}

	return true, nil
}
