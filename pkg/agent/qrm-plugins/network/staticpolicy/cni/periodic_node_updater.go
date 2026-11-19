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

package cni

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/client/control"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	NodeUpdaterHandlerName = "cni_node_updater"
	FetchInterval          = 30 * time.Second
	fetchTimeout           = 5 * time.Second
	nodeUpdateTimeout      = 5 * time.Second
)

var (
	initUpdaterOnce sync.Once
	cniNodeUpdater  *CNIPeriodicNodeUpdater
)

// CNIPeriodicNodeUpdater owns CNI fetching, its snapshot, and Node capacity updates.
// The existing periodical-handler framework schedules Refresh.
type CNIPeriodicNodeUpdater struct {
	fetcher         CNIFetcher
	nodeClient      corev1.NodeInterface
	nodeUpdater     control.NodeUpdater
	nodeName        string
	healthCheckName string

	mu       sync.RWMutex // snapshot only; never held during external I/O
	snapshot Snapshot
}

// GetCNIPeriodicNodeUpdater returns the singleton, retaining the first valid dependencies.
func GetCNIPeriodicNodeUpdater(fetcher CNIFetcher, client kubernetes.Interface, nodeName string) (*CNIPeriodicNodeUpdater, error) {
	if err := validateUpdaterDependencies(fetcher, client, nodeName); err != nil {
		return nil, err
	}
	var err error
	initUpdaterOnce.Do(func() {
		cniNodeUpdater = &CNIPeriodicNodeUpdater{
			fetcher:         fetcher,
			nodeClient:      client.CoreV1().Nodes(),
			nodeUpdater:     control.NewRealNodeUpdater(client),
			nodeName:        nodeName,
			healthCheckName: NodeUpdaterHandlerName,
			snapshot:        initialSnapshot(),
		}
	})
	return cniNodeUpdater, err
}

func validateUpdaterDependencies(fetcher CNIFetcher, client kubernetes.Interface, nodeName string) error {
	if fetcher == nil {
		return errors.New("CNI fetcher must not be nil")
	}
	if client == nil {
		return errors.New("Kubernetes client must not be nil")
	}
	if nodeName == "" {
		return errors.New("node name must not be empty")
	}
	return nil
}

func NewCNIPeriodicNodeUpdater(fetcher CNIFetcher, client kubernetes.Interface, nodeName string) (*CNIPeriodicNodeUpdater, error) {
	if err := validateUpdaterDependencies(fetcher, client, nodeName); err != nil {
		return nil, err
	}
	return &CNIPeriodicNodeUpdater{
		fetcher:         fetcher,
		nodeClient:      client.CoreV1().Nodes(),
		nodeUpdater:     control.NewRealNodeUpdater(client),
		nodeName:        nodeName,
		healthCheckName: NodeUpdaterHandlerName,
		snapshot:        initialSnapshot(),
	}, nil
}

func (u *CNIPeriodicNodeUpdater) GetSnapshot() Snapshot {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.snapshot
}

// Refresh fetches and publishes serially. A failed fetch still publishes zero.
func (u *CNIPeriodicNodeUpdater) Refresh(_ *config.Configuration, _ interface{},
	_ *dynamicconfig.DynamicAgentConfiguration, _ metrics.MetricEmitter, _ *metaserver.MetaServer,
) {
	snapshot := u.fetch()
	u.mu.Lock()
	u.snapshot = snapshot
	u.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), nodeUpdateTimeout)
	defer cancel()
	errList := []error{snapshot.Err}
	errList = append(errList, u.patchNodeStatus(ctx, snapshot.Info))
	err := utilerrors.NewAggregate(errList)
	_ = general.UpdateHealthzStateByError(u.healthCheckName, err)
	if err != nil {
		general.Errorf("update CNI capacity for node %q: %v", u.nodeName, err)
	}
}

func (u *CNIPeriodicNodeUpdater) fetch() Snapshot {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	info, err := u.fetcher.FetchCNIInfo(ctx)
	if err != nil {
		return Snapshot{Err: fmt.Errorf("fetch CNI information: %w", err)}
	}
	if err := u.validate(info); err != nil {
		return Snapshot{Err: fmt.Errorf("validate CNI information: %w", err)}
	}
	return Snapshot{Info: info}
}

func (u *CNIPeriodicNodeUpdater) validate(info CNIInfo) error {
	if info.AllocatableDirectIPs < 0 {
		return fmt.Errorf("CNI returned negative direct-IP capacity: %d", info.AllocatableDirectIPs)
	}
	return nil
}

func (u *CNIPeriodicNodeUpdater) patchNodeStatus(ctx context.Context, info CNIInfo) error {
	// Read the API object instead of a potentially stale kubelet Node cache.
	node, err := u.nodeClient.Get(ctx, u.nodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get node %q: %w", u.nodeName, err)
	}
	updated := node.DeepCopy()
	if updated.Status.Capacity == nil {
		updated.Status.Capacity = v1.ResourceList{}
	}
	updated.Status.Capacity[consts.ResourcePodIPs] = *resource.NewQuantity(info.AllocatableDirectIPs, resource.DecimalSI)
	if err := u.nodeUpdater.PatchNodeStatus(ctx, node, updated); err != nil {
		return fmt.Errorf("patch CNI capacity for node %q: %w", u.nodeName, err)
	}
	return nil
}
