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
	defaultKubeletReadOnlyPort   = 10255
	defaultRemoteRuntimeEndpoint = "unix:///run/containerd/containerd.sock"
)

const (
	defaultCheckpointManagerDir = "/var/lib/katalyst/metaserver/checkpoints"
)

type MetaServerOptions struct {
	ConfigTTL                     time.Duration
	ConfigSkipFailedDynamicUpdate bool
	ConfigCheckpointGraceTime     time.Duration

	KubeletReadOnlyPort          int
	KubeletPodCacheSyncPeriod    time.Duration
	KubeletPodCacheSyncMaxRate   int
	KubeletPodCacheSyncBurstBulk int

	RemoteRuntimeEndpoint     string
	RuntimePodCacheSyncPeriod time.Duration

	CheckpointManagerDir string
}

func NewMetaServerOptions() *MetaServerOptions {
	return &MetaServerOptions{
		ConfigTTL:                     15 * time.Second,
		ConfigSkipFailedDynamicUpdate: true,
		ConfigCheckpointGraceTime:     2 * time.Hour,
		KubeletReadOnlyPort:           defaultKubeletReadOnlyPort,
		RemoteRuntimeEndpoint:         defaultRemoteRuntimeEndpoint,
		KubeletPodCacheSyncPeriod:     30 * time.Second,
		RuntimePodCacheSyncPeriod:     30 * time.Second,
		KubeletPodCacheSyncMaxRate:    5,
		KubeletPodCacheSyncBurstBulk:  1,
		CheckpointManagerDir:          defaultCheckpointManagerDir,
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *MetaServerOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("meta-server")

	fs.DurationVar(&o.ConfigTTL, "config-ttl", o.ConfigTTL,
		"The ttl of config server fetch local config cache")
	fs.BoolVar(&o.ConfigSkipFailedDynamicUpdate, "config-skip-failed-dynamic-update", o.ConfigSkipFailedDynamicUpdate,
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
}

// ApplyTo fills up config with options
func (o *MetaServerOptions) ApplyTo(c *global.MetaServerConfiguration) error {
	c.ConfigTTL = o.ConfigTTL
	c.ConfigSkipFailedDynamicUpdate = o.ConfigSkipFailedDynamicUpdate
	c.ConfigCheckpointGraceTime = o.ConfigCheckpointGraceTime
	c.KubeletReadOnlyPort = o.KubeletReadOnlyPort
	c.RemoteRuntimeEndpoint = o.RemoteRuntimeEndpoint
	c.KubeletPodCacheSyncPeriod = o.KubeletPodCacheSyncPeriod
	c.RuntimePodCacheSyncPeriod = o.RuntimePodCacheSyncPeriod
	c.KubeletPodCacheSyncMaxRate = rate.Limit(o.KubeletPodCacheSyncMaxRate)
	c.KubeletPodCacheSyncBurstBulk = o.KubeletPodCacheSyncBurstBulk
	c.CheckpointManagerDir = o.CheckpointManagerDir
	return nil
}
