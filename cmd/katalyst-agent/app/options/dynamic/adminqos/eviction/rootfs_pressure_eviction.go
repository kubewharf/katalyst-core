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

package eviction

import (
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/eviction"
)

const (
	defaultEnableRootfsEviction                       = false
	defaultMinimumFreeThreshold                       = ""
	defaultMinimumInodesFreeThreshold                 = ""
	defaultPodMinimumUsedThreshold                    = ""
	defaultPodMinimumInodesUsedThreshold              = ""
	defaultReclaimedQoSPodUsedPriorityThreshold       = ""
	defaultReclaimedQoSPodInodesUsedPriorityThreshold = ""
	defaultMinimumImageFsDiskSizeThreshold            = "10Gi"
	defaultIgnorePrjquotaLocalStoragePath             = ""
	defaultEnableIgnorePrjquotaLocalStorage           = false

	defaultEnableRootfsOveruseEviction        = false
	defaultSharedQoSRootfsOveruseThreshold    = ""
	defaultReclaimedQoSRootfsOveruseThreshold = ""
	defaultRootfsOveruseEvictionCount         = 1
)

var (
	defaultSupportedQoSLevels       = []string{consts.PodAnnotationQoSLevelSharedCores}
	defaultSharedQoSNamespaceFilter = []string{"default"}
)

type RootfsPressureEvictionOptions struct {
	EnableRootfsPressureEviction               bool
	MinimumImageFsFreeThreshold                string
	MinimumImageFsInodesFreeThreshold          string
	PodMinimumUsedThreshold                    string
	PodMinimumInodesUsedThreshold              string
	ReclaimedQoSPodUsedPriorityThreshold       string
	ReclaimedQoSPodInodesUsedPriorityThreshold string
	MinimumImageFsDiskCapacityThreshold        string
	GracePeriod                                int64
	IgnorePrjquotaLocalStoragePath             string
	EnableIgnorePrjquotaLocalStorage           bool

	EnableRootfsOveruseEviction             bool
	RootfsOveruseEvictionSupportedQoSLevels []string
	// RootfsOveruseEviction only supports shared qos and reclaimed qos, so
	// only thresholds for shared cores and reclaimed cores can be configured.
	SharedQoSRootfsOveruseThreshold    string
	ReclaimedQoSRootfsOveruseThreshold string
	RootfsOveruseEvictionCount         int
	SharedQoSNamespaceFilter           []string
}

func NewRootfsPressureEvictionOptions() *RootfsPressureEvictionOptions {
	return &RootfsPressureEvictionOptions{
		EnableRootfsPressureEviction:               defaultEnableRootfsEviction,
		MinimumImageFsFreeThreshold:                defaultMinimumFreeThreshold,
		MinimumImageFsInodesFreeThreshold:          defaultMinimumInodesFreeThreshold,
		PodMinimumUsedThreshold:                    defaultPodMinimumUsedThreshold,
		PodMinimumInodesUsedThreshold:              defaultPodMinimumInodesUsedThreshold,
		ReclaimedQoSPodUsedPriorityThreshold:       defaultReclaimedQoSPodUsedPriorityThreshold,
		ReclaimedQoSPodInodesUsedPriorityThreshold: defaultReclaimedQoSPodInodesUsedPriorityThreshold,
		MinimumImageFsDiskCapacityThreshold:        defaultMinimumImageFsDiskSizeThreshold,
		GracePeriod:                                defaultGracePeriod,
		EnableRootfsOveruseEviction:                defaultEnableRootfsOveruseEviction,
		RootfsOveruseEvictionSupportedQoSLevels:    defaultSupportedQoSLevels,
		SharedQoSRootfsOveruseThreshold:            defaultSharedQoSRootfsOveruseThreshold,
		ReclaimedQoSRootfsOveruseThreshold:         defaultReclaimedQoSRootfsOveruseThreshold,
		RootfsOveruseEvictionCount:                 defaultRootfsOveruseEvictionCount,
		SharedQoSNamespaceFilter:                   defaultSharedQoSNamespaceFilter,
		IgnorePrjquotaLocalStoragePath:             defaultIgnorePrjquotaLocalStoragePath,
		EnableIgnorePrjquotaLocalStorage:           defaultEnableIgnorePrjquotaLocalStorage,
	}
}

func (o *RootfsPressureEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction-rootfs-pressure")
	fs.BoolVar(&o.EnableRootfsPressureEviction, "eviction-rootfs-enable", false,
		"set true to enable rootfs pressure eviction")
	fs.StringVar(&o.MinimumImageFsFreeThreshold, "eviction-rootfs-minimum-image-fs-free", "",
		"the minimum image rootfs free threshold for nodes. once the rootfs free space of current node is lower than this threshold, the eviction manager will try to evict some pods. example 200G, 10%")
	fs.StringVar(&o.MinimumImageFsInodesFreeThreshold, "eviction-rootfs-minimum-image-fs-inodes-free", "",
		"the minimum image rootfs inodes free for nodes. once the rootfs free inodes of current node is lower than this threshold, the eviction manager will try to evict some pods. example 20000, 10%")
	fs.StringVar(&o.PodMinimumUsedThreshold, "eviction-rootfs-pod-minimum-used", "",
		"the minimum rootfs used for pod. the eviction manager will ignore this pod if its rootfs used in bytes is lower than this threshold. example 500M, 1%")
	fs.StringVar(&o.PodMinimumInodesUsedThreshold, "eviction-rootfs-pod-minimum-inodes-used", "",
		"the minimum rootfs inodes used for pod. the eviction manager will ignore this pod if its rootfs inodes used is lower than this threshold. example 2000, 1%")
	fs.StringVar(&o.ReclaimedQoSPodUsedPriorityThreshold, "eviction-rootfs-reclaimed-qos-pod-used-priority", "",
		"the rootfs used priority threshold for reclaimed qos pod. the eviction manager will prioritize the eviction of offline pods that reach this threshold. example 800M, 2%")
	fs.StringVar(&o.ReclaimedQoSPodInodesUsedPriorityThreshold, "eviction-rootfs-reclaimed-qos-pod-inodes-used-priority", "",
		"the rootfs inodes used priority threshold for reclaimed qos pod. the eviction manager will prioritize the eviction of reclaimed pods that reach this threshold. example 3000, 2%")
	fs.StringVar(&o.MinimumImageFsDiskCapacityThreshold, "eviction-rootfs-minimum-image-fs-disk-capacity", "",
		"the minimum image fs disk capacity for nodes. the eviction manager will ignore those nodes whose image fs disk capacity is less than this threshold")
	fs.Int64Var(&o.GracePeriod, "eviction-rootfs-grace-period", 0,
		"the grace period of pod deletion")
	fs.StringVar(&o.IgnorePrjquotaLocalStoragePath, "eviction-rootfs-ignore-path", o.IgnorePrjquotaLocalStoragePath, "path to exclude when calculating disk pressure usage")
	fs.BoolVar(&o.EnableIgnorePrjquotaLocalStorage, "eviction-rootfs-ignore-path-enable", o.EnableIgnorePrjquotaLocalStorage, "set true to ignore prjquota path when calculating disk pressure usage")

	fs.BoolVar(&o.EnableRootfsOveruseEviction, "eviction-rootfs-overuse-enable", o.EnableRootfsOveruseEviction, "set true to enable rootfs overuse eviction")
	fs.StringSliceVar(&o.RootfsOveruseEvictionSupportedQoSLevels, "eviction-rootfs-overuse-supported-qos-levels", o.RootfsOveruseEvictionSupportedQoSLevels, "the supported qos levels for rootfs overuse eviction, supported qos levels are: shared, reclaimed")
	fs.StringVar(&o.SharedQoSRootfsOveruseThreshold, "eviction-rootfs-overuse-shared-qos-threshold", o.SharedQoSRootfsOveruseThreshold, "the shared qos rootfs overuse threshold for shared qos pods. example 500Gi, 20%")
	fs.StringVar(&o.ReclaimedQoSRootfsOveruseThreshold, "eviction-rootfs-overuse-reclaimed-qos-threshold", o.ReclaimedQoSRootfsOveruseThreshold, "the reclaimed qos rootfs overuse threshold for reclaimed qos pods. example 500Gi, 20%")
}

func (o *RootfsPressureEvictionOptions) ApplyTo(c *eviction.RootfsPressureEvictionConfiguration) error {
	c.EnableRootfsPressureEviction = o.EnableRootfsPressureEviction
	if o.MinimumImageFsFreeThreshold != "" {
		value, err := eviction.ParseThresholdValue(o.MinimumImageFsFreeThreshold)
		if err != nil {
			return errors.Wrapf(err, "failed to parse option: 'eviction-rootfs-minimum-free'")
		}
		c.MinimumImageFsFreeThreshold = value
	}
	if o.MinimumImageFsInodesFreeThreshold != "" {
		value, err := eviction.ParseThresholdValue(o.MinimumImageFsInodesFreeThreshold)
		if err != nil {
			return errors.Wrapf(err, "failed to parse option: 'eviction-rootfs-minimm-inodes-free'")
		}
		c.MinimumImageFsInodesFreeThreshold = value
	}
	if o.PodMinimumUsedThreshold != "" {
		value, err := eviction.ParseThresholdValue(o.PodMinimumUsedThreshold)
		if err != nil {
			return errors.Wrapf(err, "failed to parse option: 'eviction-rootfs-pod-minimum-used-threshold'")
		}
		c.PodMinimumUsedThreshold = value
	}
	if o.PodMinimumInodesUsedThreshold != "" {
		value, err := eviction.ParseThresholdValue(o.PodMinimumInodesUsedThreshold)
		if err != nil {
			return errors.Wrapf(err, "failed to parse option: 'eviction-rootfs-pod-minimum-inodes-used")
		}
		c.PodMinimumInodesUsedThreshold = value
	}
	if o.ReclaimedQoSPodUsedPriorityThreshold != "" {
		value, err := eviction.ParseThresholdValue(o.ReclaimedQoSPodUsedPriorityThreshold)
		if err != nil {
			return errors.Wrapf(err, "failed to parse option: 'eviction-rootfs-reclaimed-qos-pod-used-priority'")
		}
		c.ReclaimedQoSPodUsedPriorityThreshold = value
	}
	if o.ReclaimedQoSPodInodesUsedPriorityThreshold != "" {
		value, err := eviction.ParseThresholdValue(o.ReclaimedQoSPodInodesUsedPriorityThreshold)
		if err != nil {
			return errors.Wrapf(err, "failed to parse option: 'eviction-rootfs-reclaimed-qos-pod-inodes-used-priority'")
		}
		c.ReclaimedQoSPodInodesUsedPriorityThreshold = value
	}
	if o.MinimumImageFsDiskCapacityThreshold != "" {
		value, err := resource.ParseQuantity(o.MinimumImageFsDiskCapacityThreshold)
		if err != nil {
			return errors.Wrapf(err, "failed to parse option: 'eviction-rootfs-image-fs-disk-minimum-capacity'")
		}
		c.MinimumImageFsDiskCapacityThreshold = &value
	}
	c.GracePeriod = o.GracePeriod
	c.IgnorePrjquotaLocalStoragePath = o.IgnorePrjquotaLocalStoragePath
	c.EnableIgnorePrjquotaLocalStorage = o.EnableIgnorePrjquotaLocalStorage

	c.EnableRootfsOveruseEviction = o.EnableRootfsOveruseEviction
	c.RootfsOveruseEvictionSupportedQoSLevels = o.RootfsOveruseEvictionSupportedQoSLevels
	c.RootfsOveruseEvictionCount = o.RootfsOveruseEvictionCount
	c.SharedQoSNamespaceFilter = o.SharedQoSNamespaceFilter
	if o.SharedQoSRootfsOveruseThreshold != "" {
		value, err := eviction.ParseThresholdValue(o.SharedQoSRootfsOveruseThreshold)
		if err != nil {
			return errors.Wrapf(err, "failed to parse option: 'eviction-rootfs-overuse-shared-qos-rootfs-overuse-threshold'")
		}
		c.SharedQoSRootfsOveruseThreshold = value
	}
	if o.ReclaimedQoSRootfsOveruseThreshold != "" {
		value, err := eviction.ParseThresholdValue(o.ReclaimedQoSRootfsOveruseThreshold)
		if err != nil {
			return errors.Wrapf(err, "failed to parse option: 'eviction-rootfs-overuse-reclaimed-qos-rootfs-overuse-threshold'")
		}
		c.ReclaimedQoSRootfsOveruseThreshold = value
	}

	return nil
}
