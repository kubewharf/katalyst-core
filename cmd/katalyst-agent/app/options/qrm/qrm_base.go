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

import (
	cliflag "k8s.io/component-base/cli/flag"

	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

type GenericQRMPluginOptions struct {
	QRMPluginSocketDirs         []string
	StateFileDirectory          string
	ExtraStateFileAbsPath       string
	PodDebugAnnoKeys            []string
	UseKubeletReservedConfig    bool
	PodAnnotationKeptKeys       []string
	PodLabelKeptKeys            []string
	EnableReclaimNUMABinding    bool
	EnableSNBHighNumaPreference bool
}

func NewGenericQRMPluginOptions() *GenericQRMPluginOptions {
	return &GenericQRMPluginOptions{
		QRMPluginSocketDirs:   []string{"/var/lib/kubelet/plugins_registry"},
		StateFileDirectory:    "/var/lib/katalyst/qrm_advisor",
		PodDebugAnnoKeys:      []string{},
		PodAnnotationKeptKeys: []string{},
		PodLabelKeptKeys:      []string{},
	}
}

func (o *GenericQRMPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("qrm")

	fs.StringSliceVar(&o.QRMPluginSocketDirs, "qrm-socket-dirs",
		o.QRMPluginSocketDirs, "socket file directories that qrm plugins communicate witch other components")
	fs.StringVar(&o.StateFileDirectory, "qrm-state-dir", o.StateFileDirectory, "Directory that qrm plugins are using")
	fs.StringVar(&o.ExtraStateFileAbsPath, "qrm-extra-state-file", o.ExtraStateFileAbsPath, "The absolute path to an extra state file to specify cpuset.mems for specific pods")
	fs.StringSliceVar(&o.PodDebugAnnoKeys, "qrm-pod-debug-anno-keys",
		o.PodDebugAnnoKeys, "pod annotations keys to identify the pod is a debug pod, and qrm plugins will apply specific strategy to it")
	fs.BoolVar(&o.UseKubeletReservedConfig, "use-kubelet-reserved-config",
		o.UseKubeletReservedConfig, "if set true, we will prefer to use kubelet reserved config to reserved resource configuration in katalyst")
	fs.StringSliceVar(&o.PodAnnotationKeptKeys, "pod-annotation-kept-keys",
		o.PodAnnotationKeptKeys, "pod annotation keys will be kept in qrm state")
	fs.StringSliceVar(&o.PodLabelKeptKeys, "pod-label-kept-keys",
		o.PodLabelKeptKeys, "pod label keys will be kept in qrm state")
	fs.BoolVar(&o.EnableReclaimNUMABinding, "enable-reclaim-numa-binding",
		o.EnableReclaimNUMABinding, "if set true, reclaim pod will be allocated on a specific NUMA node best-effort, otherwise, reclaim pod will be allocated on multi NUMA nodes")
	fs.BoolVar(&o.EnableSNBHighNumaPreference, "enable-snb-high-numa-preference",
		o.EnableSNBHighNumaPreference, "default false,if set true, snb pod will be preferentially allocated on high numa node")
}

func (o *GenericQRMPluginOptions) ApplyTo(conf *qrmconfig.GenericQRMPluginConfiguration) error {
	conf.QRMPluginSocketDirs = o.QRMPluginSocketDirs
	conf.StateFileDirectory = o.StateFileDirectory
	conf.ExtraStateFileAbsPath = o.ExtraStateFileAbsPath
	conf.PodDebugAnnoKeys = o.PodDebugAnnoKeys
	conf.UseKubeletReservedConfig = o.UseKubeletReservedConfig
	conf.PodAnnotationKeptKeys = append(conf.PodAnnotationKeptKeys, o.PodAnnotationKeptKeys...)
	conf.PodLabelKeptKeys = append(conf.PodLabelKeptKeys, o.PodLabelKeptKeys...)
	conf.EnableReclaimNUMABinding = o.EnableReclaimNUMABinding
	conf.EnableSNBHighNumaPreference = o.EnableSNBHighNumaPreference

	return nil
}

type QRMPluginsOptions struct {
	CPUOptions     *CPUOptions
	MemoryOptions  *MemoryOptions
	NetworkOptions *NetworkOptions
	IOOptions      *IOOptions
	GPUOptions     *GPUOptions
}

func NewQRMPluginsOptions() *QRMPluginsOptions {
	return &QRMPluginsOptions{
		CPUOptions:     NewCPUOptions(),
		MemoryOptions:  NewMemoryOptions(),
		NetworkOptions: NewNetworkOptions(),
		IOOptions:      NewIOOptions(),
		GPUOptions:     NewGPUOptions(),
	}
}

func (o *QRMPluginsOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.CPUOptions.AddFlags(fss)
	o.MemoryOptions.AddFlags(fss)
	o.NetworkOptions.AddFlags(fss)
	o.IOOptions.AddFlags(fss)
	o.GPUOptions.AddFlags(fss)
}

func (o *QRMPluginsOptions) ApplyTo(conf *qrmconfig.QRMPluginsConfiguration) error {
	if err := o.CPUOptions.ApplyTo(conf.CPUQRMPluginConfig); err != nil {
		return err
	}
	if err := o.MemoryOptions.ApplyTo(conf.MemoryQRMPluginConfig); err != nil {
		return err
	}
	if err := o.NetworkOptions.ApplyTo(conf.NetworkQRMPluginConfig); err != nil {
		return err
	}
	if err := o.IOOptions.ApplyTo(conf.IOQRMPluginConfig); err != nil {
		return err
	}
	if err := o.GPUOptions.ApplyTo(conf.GPUQRMPluginConfig); err != nil {
		return err
	}
	return nil
}
