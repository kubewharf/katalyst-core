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
	QRMPluginSocketDirs           []string
	StateFileDirectory            string
	ExtraStateFileAbsPath         string
	ReclaimRelativeRootCgroupPath string
}

func NewGenericQRMPluginOptions() *GenericQRMPluginOptions {
	return &GenericQRMPluginOptions{
		QRMPluginSocketDirs:           []string{"/var/lib/kubelet/plugins_registry"},
		StateFileDirectory:            "/var/lib/katalyst/qrm_advisor",
		ReclaimRelativeRootCgroupPath: "/kubepods/besteffort",
	}
}

func (o *GenericQRMPluginOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("qrm")

	fs.StringSliceVar(&o.QRMPluginSocketDirs, "qrm-socket-dirs",
		o.QRMPluginSocketDirs, "socket file directories that qrm plugins communicate witch other components")
	fs.StringVar(&o.StateFileDirectory, "qrm-state-dir", o.StateFileDirectory, "Directory that qrm plugins are using")
	fs.StringVar(&o.ExtraStateFileAbsPath, "qrm-extra-state-file", o.ExtraStateFileAbsPath, "The absolute path to an extra state file to specify cpuset.mems for specific pods")
	fs.StringVar(&o.ReclaimRelativeRootCgroupPath,
		"reclaim-relative-root-cgroup-path", o.ReclaimRelativeRootCgroupPath,
		"top level cgroup path for reclaimed_cores qos level")
}

func (o *GenericQRMPluginOptions) ApplyTo(conf *qrmconfig.GenericQRMPluginConfiguration) error {
	conf.QRMPluginSocketDirs = o.QRMPluginSocketDirs
	conf.StateFileDirectory = o.StateFileDirectory
	conf.ExtraStateFileAbsPath = o.ExtraStateFileAbsPath
	conf.ReclaimRelativeRootCgroupPath = o.ReclaimRelativeRootCgroupPath
	return nil
}

type QRMPluginsOptions struct {
	CPUOptions     *CPUOptions
	MemoryOptions  *MemoryOptions
	NetworkOptions *NetworkOptions
}

func NewQRMPluginsOptions() *QRMPluginsOptions {
	return &QRMPluginsOptions{
		CPUOptions:     NewCPUOptions(),
		MemoryOptions:  NewMemoryOptions(),
		NetworkOptions: NewNetworkOptions(),
	}
}

func (o *QRMPluginsOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.CPUOptions.AddFlags(fss)
	o.MemoryOptions.AddFlags(fss)
	o.NetworkOptions.AddFlags(fss)
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
	return nil
}
