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

package registry

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/baseplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin/gpu"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/customdeviceplugin/rdma"
)

type initFunc func(plugin *baseplugin.BasePlugin) customdeviceplugin.CustomDevicePlugin

var customDevicePluginsMap = make(map[string]initFunc)

func RegisterCustomDevicePlugin(deviceName string, initFunc initFunc) {
	customDevicePluginsMap[deviceName] = initFunc
}

func GetRegisteredCustomDevicePlugin() map[string]initFunc {
	return customDevicePluginsMap
}

func init() {
	RegisterCustomDevicePlugin(gpu.GPUCustomDevicePluginName, gpu.NewGPUDevicePlugin)
	RegisterCustomDevicePlugin(rdma.RDMACustomDevicePluginName, rdma.NewRDMADevicePlugin)
}
