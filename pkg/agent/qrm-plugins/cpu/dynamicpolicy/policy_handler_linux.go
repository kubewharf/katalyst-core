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

package dynamicpolicy

import (
	"encoding/json"
	"fmt"
	"path"

	"github.com/google/cadvisor/container/libcontainer"
	"github.com/opencontainers/runc/libcontainer/configs"

	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
)

func (p *DynamicPolicy) applyCgroupConfigs(resp *advisorapi.ListAndWatchResponse) error {
	for _, calculationInfo := range resp.ExtraEntries {
		cgConf, ok := calculationInfo.CalculationResult.Values[string(advisorapi.ControlKnobKeyCgroupConfig)]
		if ok {
			resources := &configs.Resources{}
			err := json.Unmarshal([]byte(cgConf), resources)
			if err != nil {
				return fmt.Errorf("unmarshal %s: %s failed with error: %v",
					advisorapi.ControlKnobKeyCgroupConfig, cgConf, err)
			}
			resources.SkipDevices = true
			resources.SkipFreezeOnSet = true

			subSystems, err := libcontainer.GetCgroupSubsystems(nil)
			if err != nil {
				return fmt.Errorf("GetCgroupSubsystems failed with error: %v", err)
			}

			paths := make(map[string]string)
			for name, subsystem := range subSystems {
				paths[name] = path.Join(subsystem, calculationInfo.CgroupPath)
			}

			manager, err := libcontainer.NewCgroupManager("", paths)
			if err != nil {
				return fmt.Errorf("NewCgroupManager failed with error: %v", err)
			}
			if err := manager.Set(resources); err != nil {
				return fmt.Errorf("set %s: %s failed with error: %v",
					advisorapi.ControlKnobKeyCgroupConfig, cgConf, err)
			}
		}
	}
	return nil
}
