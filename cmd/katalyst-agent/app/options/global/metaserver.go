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
	defaultServiceProfileCacheTTL         = 1 * time.Minute
	defaultConfigCacheTTL                 = 15 * time.Second
	defaultConfigDisableDynamic           = false
	defaultConfigSkipFailedInitialization = true
	defaultConfigCheckpointGraceTime      = 2 * time.Hour
)

const (
	defaultKubeletReadOnlyPort          = 10255
	defaultKubeletSecurePort            = 10250
	defaultEnableKubeletSecurePort      = false
	defaultRemoteRuntimeEndpoint        = "unix:///run/containerd/containerd.sock"
	defaultKubeletPodCacheSyncPeriod    = 30 * time.Second
	defaultRuntimePodCacheSyncPeriod    = 30 * time.Second
	defaultKubeletPodCacheSyncMaxRate   = 5
	defaultKubeletPodCacheSyncBurstBulk = 1
	defaultKubeletConfigURI             = "/configz"
	defaultAPIAuthTokenFile             = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultKubeletPodsEndpoint          = "/pods"
	defaultKubeletSummaryEndpoint       = "/stats/summary"
	defaultMetricInsurancePeriod        = 120 * time.Second
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
	KubeletSecurePort            int
	EnableKubeletSecurePort      bool
	KubeletPodCacheSyncPeriod    time.Duration
	KubeletPodCacheSyncMaxRate   int
	KubeletPodCacheSyncBurstBulk int
	KubeletConfigEndpoint        string
	APIAuthTokenFile             string
	KubeletPodsEndpoint          string
	KubeletSummaryEndpoint       string

	RemoteRuntimeEndpoint     string
	RuntimePodCacheSyncPeriod time.Duration

	CheckpointManagerDir string

	EnableMetricsFetcher bool
	EnableCNCFetcher     bool

	MetricInsurancePeriod time.Duration
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
		KubeletSecurePort:              defaultKubeletSecurePort,
		EnableKubeletSecurePort:        defaultEnableKubeletSecurePort,
		RemoteRuntimeEndpoint:          defaultRemoteRuntimeEndpoint,
		KubeletPodCacheSyncPeriod:      defaultKubeletPodCacheSyncPeriod,
		RuntimePodCacheSyncPeriod:      defaultRuntimePodCacheSyncPeriod,
		KubeletPodCacheSyncMaxRate:     defaultKubeletPodCacheSyncMaxRate,
		KubeletPodCacheSyncBurstBulk:   defaultKubeletPodCacheSyncBurstBulk,
		CheckpointManagerDir:           defaultCheckpointManagerDir,
		EnableMetricsFetcher:           defaultEnableMetricsFetcher,
		EnableCNCFetcher:               defaultEnableCNCFetcher,
		KubeletConfigEndpoint:          defaultKubeletConfigURI,
		KubeletPodsEndpoint:            defaultKubeletPodsEndpoint,
		KubeletSummaryEndpoint:         defaultKubeletSummaryEndpoint,
		APIAuthTokenFile:               defaultAPIAuthTokenFile,
		MetricInsurancePeriod:          defaultMetricInsurancePeriod,
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
	fs.IntVar(&o.KubeletSecurePort, "kubelet-secure-port", o.KubeletSecurePort,
		"The secure port for the kubelet to serve")
	fs.BoolVar(&o.EnableKubeletSecurePort, "enable-kubelet-secure-port", o.EnableKubeletSecurePort,
		"Whether to enable get contents from kubelet secure port")
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
	fs.StringVar(&o.KubeletConfigEndpoint, "kubelet-config-endpoint", o.KubeletConfigEndpoint,
		"The URI of kubelet config endpoint")
	fs.StringVar(&o.KubeletPodsEndpoint, "kubelet-pods-endpoint", o.KubeletPodsEndpoint,
		"The URI of kubelet pods endpoint")
	fs.StringVar(&o.KubeletSummaryEndpoint, "kubelet-summary-endpoint", o.KubeletSummaryEndpoint,
		"The URI of kubelet summary endpoint")
	fs.StringVar(&o.APIAuthTokenFile, "api-auth-token-file", o.APIAuthTokenFile,
		"The path of the API auth token file")
	fs.DurationVar(&o.MetricInsurancePeriod, "metric-insurance-period", o.MetricInsurancePeriod,
		"The meta server return metric data and MetricDataExpired if the update time of metric data is earlier than this period.")
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
	c.EnableKubeletSecurePort = o.EnableKubeletSecurePort
	c.KubeletSecurePort = o.KubeletSecurePort
	c.RemoteRuntimeEndpoint = o.RemoteRuntimeEndpoint
	c.KubeletPodCacheSyncPeriod = o.KubeletPodCacheSyncPeriod
	c.RuntimePodCacheSyncPeriod = o.RuntimePodCacheSyncPeriod
	c.KubeletPodCacheSyncMaxRate = rate.Limit(o.KubeletPodCacheSyncMaxRate)
	c.KubeletPodCacheSyncBurstBulk = o.KubeletPodCacheSyncBurstBulk
	c.CheckpointManagerDir = o.CheckpointManagerDir
	c.EnableMetricsFetcher = o.EnableMetricsFetcher
	c.EnableCNCFetcher = o.EnableCNCFetcher
	c.KubeletConfigEndpoint = o.KubeletConfigEndpoint
	c.KubeletPodsEndpoint = o.KubeletPodsEndpoint
	c.KubeletSummaryEndpoint = o.KubeletSummaryEndpoint
	c.APIAuthTokenFile = o.APIAuthTokenFile
	c.MetricInsurancePeriod = o.MetricInsurancePeriod

	return nil
}
