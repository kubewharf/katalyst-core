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

package resourcepackage

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/pkg/errors"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/npd"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

const (
	updateConfigInterval     = 5 * time.Second
	updateConfigJitterFactor = 0.5
)

const (
	metricsNameUpdateNPD         = "metaserver_update_npd"
	metricsNameLoadNPDCheckpoint = "metaserver_load_npd_checkpoint"

	metricsValueStatusCheckpointNotFoundOrCorrupted = "notFoundOrCorrupted"
	metricsValueStatusCheckpointInvalidOrExpired    = "invalidOrExpired"
	metricsValueStatusCheckpointSuccess             = "success"
)

const (
	resourcePackageManagerCheckpoint = "resource_package_manager_checkpoint"
)

// ResourcePackageManager provides access to a node's resource package division
type ResourcePackageManager interface {
	// NodeResourcePackages returns the resource package division for the
	// specified node. The returned map's keys are NUMA IDs (as int)
	// and the values are slices of ResourcePackage belonging to that
	// NUMA node: map[NUMA ID] -> []nodev1alpha1.ResourcePackage.
	NodeResourcePackages(ctx context.Context) (map[int][]nodev1alpha1.ResourcePackage, error)

	// ConvertNPDResourcePackages converts a given NodeProfileDescriptor to
	// resource packages. The returned map's keys are NUMA IDs (as int)
	// and the values are slices of ResourcePackage belonging to that
	// NUMA node: map[NUMA ID] -> []nodev1alpha1.ResourcePackage.
	ConvertNPDResourcePackages(npd *nodev1alpha1.NodeProfileDescriptor) (map[int][]nodev1alpha1.ResourcePackage, error)

	// Run starts a loop that periodically fetches the latest NPD and updates
	Run(ctx context.Context)
}

type DummyResourcePackageManager struct{}

func (d *DummyResourcePackageManager) NodeResourcePackages(ctx context.Context) (map[int][]nodev1alpha1.ResourcePackage, error) {
	return map[int][]nodev1alpha1.ResourcePackage{}, nil
}

func (d *DummyResourcePackageManager) ConvertNPDResourcePackages(npd *nodev1alpha1.NodeProfileDescriptor) (map[int][]nodev1alpha1.ResourcePackage, error) {
	return map[int][]nodev1alpha1.ResourcePackage{}, nil
}

func (d *DummyResourcePackageManager) Run(ctx context.Context) {}

// resourcePackageManager is the default implementation of ResourcePackageManager
type resourcePackageManager struct {
	// fetcher provides access to node-level package information (from
	// the NPD component).
	fetcher npd.NPDFetcher

	// npd is the latest fetched NodeProfileDescriptor
	npd *nodev1alpha1.NodeProfileDescriptor

	// checkpoint stores recent fetched NPDs
	checkpointManager checkpointmanager.CheckpointManager

	// checkpointGraceTime is the duration to consider a checkpoint valid
	checkpointGraceTime time.Duration

	// emitter is the metric emitter
	emitter metrics.MetricEmitter
}

// NodeResourcePackages returns the resource package division for the
// specified node. The returned map's keys are NUMA IDs (as int)
// and the values are slices of ResourcePackage belonging to that
// NUMA node: map[NUMA ID] -> []nodev1alpha1.ResourcePackage.
func (m *resourcePackageManager) NodeResourcePackages(ctx context.Context) (map[int][]nodev1alpha1.ResourcePackage, error) {
	if m.npd == nil {
		return nil, errors.Errorf("failed to get npd")
	}
	return m.ConvertNPDResourcePackages(m.npd)
}

// ConvertNPDResourcePackages converts a given NodeProfileDescriptor to
// resource packages. The returned map's keys are NUMA IDs (as int)
// and the values are slices of ResourcePackage belonging to that
// NUMA node: map[NUMA ID] -> []nodev1alpha1.ResourcePackage
func (m *resourcePackageManager) ConvertNPDResourcePackages(npd *nodev1alpha1.NodeProfileDescriptor) (map[int][]nodev1alpha1.ResourcePackage, error) {
	resourcePackageMetrics := resourcepackage.ConvertNPDMetricsToResourcePackages(npd.Status.NodeMetrics)
	resourcePackageMap := make(map[int][]nodev1alpha1.ResourcePackage)

	var errList []error
	for _, metric := range resourcePackageMetrics {
		numaID, err := strconv.Atoi(metric.NumaID)
		if err != nil {
			errList = append(errList, errors.Wrap(err, "numa ID invalid"))
			continue
		}
		resourcePackageMap[numaID] = metric.ResourcePackages
	}
	return resourcePackageMap, apierrors.NewAggregate(errList)
}

// Run starts a loop that periodically fetches the latest NPD and updates
func (m *resourcePackageManager) Run(ctx context.Context) {
	go wait.JitterUntilWithContext(ctx, func(context.Context) {
		if err := m.updateNodeProfileDescriptor(ctx); err != nil {
			klog.Errorf("[resourcepackage-manager] update node profile descriptor failed: %v", err)
		}
	}, updateConfigInterval, updateConfigJitterFactor, true)
	<-ctx.Done()
}

// NewResourcePackageManager creates a new ResourcePackageManager that uses the provided NPD fetcher.
func NewResourcePackageManager(fetcher npd.NPDFetcher, conf *pkgconfig.Configuration, emitter metrics.MetricEmitter) (ResourcePackageManager, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(conf.CheckpointManagerDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}

	return &resourcePackageManager{
		fetcher:             fetcher,
		checkpointManager:   checkpointManager,
		checkpointGraceTime: conf.ConfigCheckpointGraceTime,
		emitter:             emitter,
	}, nil
}

// readCheckpoint reads the checkpoint from disk
func (m *resourcePackageManager) readCheckpoint() (NodeProfileCheckpoint, error) {
	cp := NewCheckpoint(NodeProfileData{})
	if err := m.checkpointManager.GetCheckpoint(resourcePackageManagerCheckpoint, cp); err != nil {
		klog.Errorf("[resourcepackage-manager] failed to get resource package manager checkpoint: %v", err)
		return nil, err
	}
	return cp, nil
}

// writeCheckpoint writes the checkpoint to disk
func (m *resourcePackageManager) writeCheckpoint(npd *nodev1alpha1.NodeProfileDescriptor) {
	data, err := m.readCheckpoint()
	if err != nil {
		klog.Errorf("[resourcepackage-manager] load checkpoint from %q failed: %v, try to overwrite it", resourcePackageManagerCheckpoint, err)
		_ = m.emitter.StoreInt64(metricsNameLoadNPDCheckpoint, 1, metrics.MetricTypeNameRaw, []metrics.MetricTag{
			{Key: "status", Val: metricsValueStatusCheckpointNotFoundOrCorrupted},
			{Key: "node", Val: npd.Name},
		}...)
	}
	// checkpoint doesn't exist or became corrupted, make a new checkpoint
	if data == nil {
		data = NewCheckpoint(NodeProfileData{})
	}

	// set config value and timestamp for kind
	data.SetProfile(npd, metav1.Now())
	err = m.checkpointManager.CreateCheckpoint(resourcePackageManagerCheckpoint, data)
	if err != nil {
		klog.Errorf("[resourcepackage-manager] failed to write checkpoint file %q: %v", resourcePackageManagerCheckpoint, err)
	}
}

// updateNodeProfileDescriptor tries to fetch the latest NPD from the fetcher.
func (m *resourcePackageManager) updateNodeProfileDescriptor(ctx context.Context) error {
	// try to get from fetcher first
	latestNpd, err := m.fetcher.GetNPD(ctx)
	if err != nil || latestNpd == nil {
		klog.Errorf("[resourcepackage-manager] try get new npd error: %v", err)
		// load from checkpoint if fetcher fails
		data, err := m.readCheckpoint()
		if err != nil {
			_ = m.emitter.StoreInt64(metricsNameUpdateNPD, 1, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: "status", Val: metricsValueStatusCheckpointNotFoundOrCorrupted},
			}...)
			return errors.Wrap(err, "failed to load npd checkpoint")
		}
		npdData, timestamp := data.GetProfile()
		if time.Now().Before(timestamp.Add(m.checkpointGraceTime)) && npdData != nil {
			latestNpd = npdData
			klog.Infof("[resourcepackage-manager] failed to load npd from remote, use local checkpoint instead")
			_ = m.emitter.StoreInt64(metricsNameLoadNPDCheckpoint, 1, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: "status", Val: metricsValueStatusCheckpointSuccess},
				{Key: "npd", Val: latestNpd.Name},
			}...)
		} else {
			_ = m.emitter.StoreInt64(metricsNameLoadNPDCheckpoint, 1, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: "status", Val: metricsValueStatusCheckpointInvalidOrExpired},
				{Key: "npd", Val: "invalid"},
			}...)
			return fmt.Errorf("failed to load npd checkpoint, checkpoint expired or invalid")
		}
	}

	if !apiequality.Semantic.DeepEqual(m.npd, latestNpd) {
		m.npd = latestNpd
	}
	m.writeCheckpoint(latestNpd)
	return nil
}
