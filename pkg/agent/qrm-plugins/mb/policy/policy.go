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

package policy

import (
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	vendorAMD = "AuthenticAMD"
)

func isSupported(cpuVendorID string) bool {
	// only AMD is supported
	return cpuVendorID == vendorAMD
}

func NewGenericPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	general.Infof("mbm: to create generic policy qrm_mb_plugin")

	cpuVendor := agentCtx.CPUVendorID
	if !isSupported(cpuVendor) {
		general.Infof("mbm: unsupported cpu arch %s", cpuVendor)
		return false, nil, nil
	}

	mbPlugin := newMBPlugin()
	return true, &agent.PluginWrapper{GenericPlugin: mbPlugin}, nil
}
