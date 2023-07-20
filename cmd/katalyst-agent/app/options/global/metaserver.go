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

package global

import (
	"time"

	"golang.org/x/time/rate"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

const (
	defaultCustomNodeResourceCacheTTL     = 15 * time.Second
	defaultCustomNodeConfigCacheTTL       = 15 * time.Second
	defaultServiceProfileCacheTTL         = 15 * time.Second
	defaultConfigCacheTTL                 = 15 * time.Second
	defaultConfigDisableDynamic           = false
	defaultConfigSkipFailedInitialization = true
	defaultConfigCheckpointGraceTime      = 2 * time.Hour
)

const (
	defaultKubeletReadOnlyPort          = 10255
	defaultRemoteRuntimeEndpoint        = "unix:///run/containerd/containerd.sock"
	defaultKubeletPodCacheSyncPeriod    = 30 * time.Second
	defaultRuntimePodCacheSyncPeriod    = 30 * time.Second
	defaultKubeletPodCacheSyncMaxRate   = 5
	defaultKubeletPodCacheSyncBurstBulk = 1
)

const (
	defaultCheckpointManagerDir = "/var/lib/katalyst/metaserver/checkpoints"
)

const (
	defaultEnableMetricsFetcher = true
	defaultEnableCNCFetcher     = true
)

type MetaServerOptions struct {
	CNRCacheTTL                    time.Duration
	CustomNodeConfigCacheTTL       time.Duration
	ServiceProfileCacheTTL         time.Duration
	ConfigCacheTTL                 time.Duration
	ConfigDisableDynamic           bool
	ConfigSkipFailedInitialization bool
	ConfigCheckpointGraceTime      time.Duration

	KubeletReadOnlyPort          int
	KubeletPodCacheSyncPeriod    time.Duration
	KubeletPodCacheSyncMaxRate   int
	KubeletPodCacheSyncBurstBulk int

	RemoteRuntimeEndpoint     string
	RuntimePodCacheSyncPeriod time.Duration

	CheckpointManagerDir string

	EnableMetricsFetcher bool
	EnableCNCFetcher     bool
}

func NewMetaServerOptions() *MetaServerOptions {
	return &MetaServerOptions{
		CNRCacheTTL:                    defaultCustomNodeResourceCacheTTL,
		CustomNodeConfigCacheTTL:       defaultCustomNodeConfigCacheTTL,
		ServiceProfileCacheTTL:         defaultServiceProfileCacheTTL,
		ConfigCacheTTL:                 defaultConfigCacheTTL,
		ConfigDisableDynamic:           defaultConfigDisableDynamic,
		ConfigSkipFailedInitialization: defaultConfigSkipFailedInitialization,
		ConfigCheckpointGraceTime:      defaultConfigCheckpointGraceTime,
		KubeletReadOnlyPort:            defaultKubeletReadOnlyPort,
		RemoteRuntimeEndpoint:          defaultRemoteRuntimeEndpoint,
		KubeletPodCacheSyncPeriod:      defaultKubeletPodCacheSyncPeriod,
		RuntimePodCacheSyncPeriod:      defaultRuntimePodCacheSyncPeriod,
		KubeletPodCacheSyncMaxRate:     defaultKubeletPodCacheSyncMaxRate,
		KubeletPodCacheSyncBurstBulk:   defaultKubeletPodCacheSyncBurstBulk,
		CheckpointManagerDir:           defaultCheckpointManagerDir,
		EnableMetricsFetcher:           defaultEnableMetricsFetcher,
		EnableCNCFetcher:               defaultEnableCNCFetcher,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *MetaServerOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("meta-server")

	fs.DurationVar(&o.CNRCacheTTL, "cnr-cache-ttl", o.CNRCacheTTL,
		"The sync period of cnr fetcher to sync remote to local")
	fs.DurationVar(&o.CustomNodeConfigCacheTTL, "custom-node-config-cache-ttl", o.CustomNodeConfigCacheTTL,
		"The ttl of custom node config fetcher cache remote cnc")
	fs.DurationVar(&o.ServiceProfileCacheTTL, "service-profile-cache-ttl", o.ServiceProfileCacheTTL,
		"The ttl of service profile manager cache remote spd")
	fs.DurationVar(&o.ConfigCacheTTL, "config-cache-ttl", o.ConfigCacheTTL,
		"The ttl of katalyst custom config loader cache remote config")
	fs.BoolVar(&o.ConfigDisableDynamic, "config-disable-dynamic", o.ConfigDisableDynamic,
		"Whether disable dynamic configuration")
	fs.BoolVar(&o.ConfigSkipFailedInitialization, "config-skip-failed-initialization", o.ConfigSkipFailedInitialization,
		"Whether skip if updating dynamic configuration fails")
	fs.DurationVar(&o.ConfigCheckpointGraceTime, "config-checkpoint-grace-time", o.ConfigCheckpointGraceTime,
		"The grace time of meta server config checkpoint")
	fs.IntVar(&o.KubeletReadOnlyPort, "kubelet-read-only-port", o.KubeletReadOnlyPort,
		"The read-only port for the kubelet to serve")
	fs.StringVar(&o.RemoteRuntimeEndpoint, "remote-runtime-endpoint", o.RemoteRuntimeEndpoint,
		"The endpoint of remote runtime service")
	fs.DurationVar(&o.KubeletPodCacheSyncPeriod, "kubelet-pod-cache-sync-period", o.KubeletPodCacheSyncPeriod,
		"The period of meta server to sync pod from kubelet 10255 port")
	fs.DurationVar(&o.RuntimePodCacheSyncPeriod, "runtime-pod-cache-sync-period", o.RuntimePodCacheSyncPeriod,
		"The period of meta server to sync pod from cri")
	fs.IntVar(&o.KubeletPodCacheSyncMaxRate, "kubelet-pod-cache-sync-max-rate", o.KubeletPodCacheSyncMaxRate,
		"The max rate for kubelet pod sync")
	fs.IntVar(&o.KubeletPodCacheSyncBurstBulk, "kubelet-pod-cache-sync-burst-bulk", o.KubeletPodCacheSyncBurstBulk,
		"The burst bulk for kubelet pod sync")
	fs.StringVar(&o.CheckpointManagerDir, "checkpoint-manager-directory", o.CheckpointManagerDir,
		"The checkpoint manager directory")
	fs.BoolVar(&o.EnableMetricsFetcher, "enable-metrics-fetcher", o.EnableMetricsFetcher,
		"Whether to enable metrics fetcher")
	fs.BoolVar(&o.EnableCNCFetcher, "enable-cnc-fetcher", o.EnableCNCFetcher,
		"Whether to enable cnc fetcher")
}

// ApplyTo fills up config with options
func (o *MetaServerOptions) ApplyTo(c *global.MetaServerConfiguration) error {
	c.CNRCacheTTL = o.CNRCacheTTL
	c.CustomNodeConfigCacheTTL = o.CustomNodeConfigCacheTTL
	c.ServiceProfileCacheTTL = o.ServiceProfileCacheTTL
	c.ConfigCacheTTL = o.ConfigCacheTTL
	c.ConfigDisableDynamic = o.ConfigDisableDynamic
	c.ConfigSkipFailedInitialization = o.ConfigSkipFailedInitialization
	c.ConfigCheckpointGraceTime = o.ConfigCheckpointGraceTime
	c.KubeletReadOnlyPort = o.KubeletReadOnlyPort
	c.RemoteRuntimeEndpoint = o.RemoteRuntimeEndpoint
	c.KubeletPodCacheSyncPeriod = o.KubeletPodCacheSyncPeriod
	c.RuntimePodCacheSyncPeriod = o.RuntimePodCacheSyncPeriod
	c.KubeletPodCacheSyncMaxRate = rate.Limit(o.KubeletPodCacheSyncMaxRate)
	c.KubeletPodCacheSyncBurstBulk = o.KubeletPodCacheSyncBurstBulk
	c.CheckpointManagerDir = o.CheckpointManagerDir
	c.EnableMetricsFetcher = o.EnableMetricsFetcher
	c.EnableCNCFetcher = o.EnableCNCFetcher
	return nil
}
