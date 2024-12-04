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

package qrm

import "github.com/kubewharf/katalyst-api/pkg/consts"

type GenericQRMPluginConfiguration struct {
	StateFileDirectory       string
	QRMPluginSocketDirs      []string
	ExtraStateFileAbsPath    string
	PodDebugAnnoKeys         []string
	UseKubeletReservedConfig bool
	// PodAnnotationKeptKeys indicates pod annotation keys will be kept in qrm state
	PodAnnotationKeptKeys []string
	// PodLabelKeptKeys indicates pod label keys will be kept in qrm state
	PodLabelKeptKeys []string
	// EnableReclaimNUMABinding indicates whether to enable NUMA Binding for reclaim pods
	// if this flag is set to true, reclaim pod will be allocated on a specific NUMA node
	// best-effort, otherwise, reclaim pod will be allocated on multi NUMA nodes
	EnableReclaimNUMABinding bool
}

type QRMPluginsConfiguration struct {
	*CPUQRMPluginConfig
	*MemoryQRMPluginConfig
	*NetworkQRMPluginConfig
	*IOQRMPluginConfig
}

func NewGenericQRMPluginConfiguration() *GenericQRMPluginConfiguration {
	return &GenericQRMPluginConfiguration{
		PodAnnotationKeptKeys: []string{
			consts.PodAnnotationAggregatedRequestsKey,
			consts.PodAnnotationInplaceUpdateResizingKey,
		},
		PodLabelKeptKeys: []string{},
	}
}

func NewQRMPluginsConfiguration() *QRMPluginsConfiguration {
	return &QRMPluginsConfiguration{
		CPUQRMPluginConfig:     NewCPUQRMPluginConfig(),
		MemoryQRMPluginConfig:  NewMemoryQRMPluginConfig(),
		NetworkQRMPluginConfig: NewNetworkQRMPluginConfig(),
		IOQRMPluginConfig:      NewIOQRMPluginConfig(),
	}
}
