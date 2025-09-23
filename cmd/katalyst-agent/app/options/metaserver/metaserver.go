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

package metaserver

import (
	"time"

	"golang.org/x/time/rate"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/metaserver"
)

const (
	defaultCheckpointManagerDir = "/var/lib/katalyst/metaserver/checkpoints"
	defaultEnableMetricsFetcher = true
	defaultEnableCNCFetcher     = true
	defaultEnableNPDFetcher     = true
)

const (
	defaultConfigCacheTTL                 = 15 * time.Second
	defaultConfigDisableDynamic           = false
	defaultConfigSkipFailedInitialization = true
	defaultConfigCheckpointGraceTime      = 2 * time.Hour
)

const (
	defaultServiceProfileSkipCorruptionError = true
	defaultServiceProfileCacheTTL            = 1 * time.Minute
	defaultSPDGetFromRemote                  = false
)

const (
	defaultKubeletPodCacheSyncPeriod         = 30 * time.Second
	defaultKubeletPodCacheSyncMaxRate        = 5
	defaultKubeletPodCacheSyncBurstBulk      = 1
	defaultKubeletPodCacheSyncEmptyThreshold = 5
	defaultRuntimePodCacheSyncPeriod         = 30 * time.Second
)

const defaultCustomNodeResourceCacheTTL = 15 * time.Second

const defaultCustomNodeConfigCacheTTL = 15 * time.Second

// MetaServerOptions holds all the configurations for metaserver.
// we will not try to separate this structure into several individual
// structures since it will not be used directly by other components; instead,
// we will only separate them with blanks in a single structure.
// todo separate this option-structure into individual structures
type MetaServerOptions struct {
	// generic configurations for metaserver
	CheckpointManagerDir string
	EnableMetricsFetcher bool
	EnableCNCFetcher     bool
	EnableNPDFetcher     bool

	// configurations for kcc
	ConfigCacheTTL                 time.Duration
	ConfigDisableDynamic           bool
	ConfigSkipFailedInitialization bool
	ConfigCheckpointGraceTime      time.Duration

	// configurations for spd
	ServiceProfileEnableNamespaces    []string
	ServiceProfileSkipCorruptionError bool
	ServiceProfileCacheTTL            time.Duration
	SPDGetFromRemote                  bool

	// configurations for pod-cache
	KubeletPodCacheSyncPeriod         time.Duration
	KubeletPodCacheSyncMaxRate        int
	KubeletPodCacheSyncBurstBulk      int
	KubeletPodCacheSyncEmptyThreshold int
	RuntimePodCacheSyncPeriod         time.Duration

	// configurations for cnr
	CNRCacheTTL time.Duration

	// configurations for cnc
	CustomNodeConfigCacheTTL time.Duration

	// configurations for metric-fetcher
	*MetricFetcherOptions
}

func NewMetaServerOptions() *MetaServerOptions {
	return &MetaServerOptions{
		CheckpointManagerDir: defaultCheckpointManagerDir,
		EnableMetricsFetcher: defaultEnableMetricsFetcher,
		EnableCNCFetcher:     defaultEnableCNCFetcher,
		EnableNPDFetcher:     defaultEnableNPDFetcher,

		ConfigCacheTTL:                 defaultConfigCacheTTL,
		ConfigDisableDynamic:           defaultConfigDisableDynamic,
		ConfigSkipFailedInitialization: defaultConfigSkipFailedInitialization,
		ConfigCheckpointGraceTime:      defaultConfigCheckpointGraceTime,

		ServiceProfileEnableNamespaces:    []string{"*"},
		ServiceProfileSkipCorruptionError: defaultServiceProfileSkipCorruptionError,
		ServiceProfileCacheTTL:            defaultServiceProfileCacheTTL,
		SPDGetFromRemote:                  defaultSPDGetFromRemote,

		KubeletPodCacheSyncPeriod:         defaultKubeletPodCacheSyncPeriod,
		KubeletPodCacheSyncMaxRate:        defaultKubeletPodCacheSyncMaxRate,
		KubeletPodCacheSyncBurstBulk:      defaultKubeletPodCacheSyncBurstBulk,
		KubeletPodCacheSyncEmptyThreshold: defaultKubeletPodCacheSyncEmptyThreshold,
		RuntimePodCacheSyncPeriod:         defaultRuntimePodCacheSyncPeriod,

		CNRCacheTTL: defaultCustomNodeResourceCacheTTL,

		CustomNodeConfigCacheTTL: defaultCustomNodeConfigCacheTTL,

		MetricFetcherOptions: NewMetricFetcherOptions(),
	}
}

// AddFlags adds flags to the specified FlagSet.
func (o *MetaServerOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("meta-server")

	fs.StringVar(&o.CheckpointManagerDir, "checkpoint-manager-directory", o.CheckpointManagerDir,
		"The checkpoint manager directory")
	fs.BoolVar(&o.EnableMetricsFetcher, "enable-metrics-fetcher", o.EnableMetricsFetcher,
		"Whether to enable metrics fetcher")
	fs.BoolVar(&o.EnableCNCFetcher, "enable-cnc-fetcher", o.EnableCNCFetcher,
		"Whether to enable cnc fetcher")
	fs.BoolVar(&o.EnableNPDFetcher, "enable-npd-fetcher", o.EnableNPDFetcher,
		"Whether to enable npd fetcher")

	fs.StringSliceVar(&o.ServiceProfileEnableNamespaces, "service-profile-enable-namespaces", o.ServiceProfileEnableNamespaces,
		"Comma-separated list of namespaces where service profiles are enabled, default is all namespaces")
	fs.DurationVar(&o.ConfigCacheTTL, "config-cache-ttl", o.ConfigCacheTTL,
		"The ttl of katalyst custom config loader cache remote config")
	fs.BoolVar(&o.ConfigDisableDynamic, "config-disable-dynamic", o.ConfigDisableDynamic,
		"Whether disable dynamic configuration")
	fs.BoolVar(&o.ConfigSkipFailedInitialization, "config-skip-failed-initialization", o.ConfigSkipFailedInitialization,
		"Whether skip if updating dynamic configuration fails")
	fs.DurationVar(&o.ConfigCheckpointGraceTime, "config-checkpoint-grace-time", o.ConfigCheckpointGraceTime,
		"The grace time of meta server config checkpoint")

	fs.BoolVar(&o.ServiceProfileSkipCorruptionError, "service-profile-skip-corruption-error", o.ServiceProfileSkipCorruptionError,
		"Whether to skip corruption error when loading spd checkpoint")
	fs.DurationVar(&o.ServiceProfileCacheTTL, "service-profile-cache-ttl", o.ServiceProfileCacheTTL,
		"The ttl of service profile manager cache remote spd")
	fs.BoolVar(&o.SPDGetFromRemote, "spd-get-from-remote", o.SPDGetFromRemote, "get spd from remote if not in cache")

	fs.DurationVar(&o.KubeletPodCacheSyncPeriod, "kubelet-pod-cache-sync-period", o.KubeletPodCacheSyncPeriod,
		"The period of meta server to sync pod from kubelet 10255 port")
	fs.IntVar(&o.KubeletPodCacheSyncMaxRate, "kubelet-pod-cache-sync-max-rate", o.KubeletPodCacheSyncMaxRate,
		"The max rate for kubelet pod sync")
	fs.IntVar(&o.KubeletPodCacheSyncBurstBulk, "kubelet-pod-cache-sync-burst-bulk", o.KubeletPodCacheSyncBurstBulk,
		"The burst bulk for kubelet pod sync")
	fs.IntVar(&o.KubeletPodCacheSyncEmptyThreshold, "kubelet-pod-cache-sync-empty-threshold", o.KubeletPodCacheSyncEmptyThreshold,
		"The threshold for kubelet returns empty pod list, so that empty error can be skipped")
	fs.DurationVar(&o.RuntimePodCacheSyncPeriod, "runtime-pod-cache-sync-period", o.RuntimePodCacheSyncPeriod,
		"The period of meta server to sync pod from cri")

	fs.DurationVar(&o.CNRCacheTTL, "cnr-cache-ttl", o.CNRCacheTTL,
		"The sync period of cnr fetcher to sync remote to local")

	fs.DurationVar(&o.CustomNodeConfigCacheTTL, "custom-node-config-cache-ttl", o.CustomNodeConfigCacheTTL,
		"The ttl of custom node config fetcher cache remote cnc")

	o.MetricFetcherOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *MetaServerOptions) ApplyTo(c *metaserver.MetaServerConfiguration) error {
	c.CheckpointManagerDir = o.CheckpointManagerDir
	c.EnableMetricsFetcher = o.EnableMetricsFetcher
	c.EnableCNCFetcher = o.EnableCNCFetcher
	c.EnableNPDFetcher = o.EnableNPDFetcher

	c.ConfigCacheTTL = o.ConfigCacheTTL
	c.ConfigDisableDynamic = o.ConfigDisableDynamic
	c.ConfigSkipFailedInitialization = o.ConfigSkipFailedInitialization
	c.ConfigCheckpointGraceTime = o.ConfigCheckpointGraceTime

	c.ServiceProfileEnableNamespaces = o.ServiceProfileEnableNamespaces
	c.ServiceProfileSkipCorruptionError = o.ServiceProfileSkipCorruptionError
	c.ServiceProfileCacheTTL = o.ServiceProfileCacheTTL
	c.SPDGetFromRemote = o.SPDGetFromRemote

	c.KubeletPodCacheSyncPeriod = o.KubeletPodCacheSyncPeriod
	c.KubeletPodCacheSyncMaxRate = rate.Limit(o.KubeletPodCacheSyncMaxRate)
	c.KubeletPodCacheSyncBurstBulk = o.KubeletPodCacheSyncBurstBulk
	c.KubeletPodCacheSyncEmptyThreshold = o.KubeletPodCacheSyncEmptyThreshold
	c.RuntimePodCacheSyncPeriod = o.RuntimePodCacheSyncPeriod

	c.CNRCacheTTL = o.CNRCacheTTL

	c.CustomNodeConfigCacheTTL = o.CustomNodeConfigCacheTTL

	if err := o.MetricFetcherOptions.ApplyTo(c.MetricConfiguration); err != nil {
		return err
	}

	return nil
}
