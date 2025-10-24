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

package npd

import (
	"context"
	"fmt"
	"time"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	pkgconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnc"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/kcc"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

const (
	metricsNameUpdateNPD         = "metaserver_update_npd"
	metricsNameLoadNPDCheckpoint = "metaserver_load_npd_checkpoint"

	metricsValueStatusCheckpointNotFoundOrCorrupted = "notFoundOrCorrupted"
	metricsValueStatusCheckpointInvalidOrExpired    = "invalidOrExpired"
	metricsValueStatusCheckpointSuccess             = "success"
)

const (
	npdFetcherCheckpoint = "npd_fetcher_checkpoint"
)

type NPDFetcher interface {
	GetNPD(ctx context.Context) (*v1alpha1.NodeProfileDescriptor, error)
}

type DummyNPDFetcher struct {
	NPD *v1alpha1.NodeProfileDescriptor
}

func (f *DummyNPDFetcher) GetNPD(_ context.Context) (*v1alpha1.NodeProfileDescriptor, error) {
	return f.NPD, nil
}

type npdFetcher struct {
	configLoader kcc.ConfigurationLoader

	// checkpoint stores recent fetched NPDs
	checkpointManager checkpointmanager.CheckpointManager

	// checkpointGraceTime is the duration to consider a checkpoint valid
	checkpointGraceTime time.Duration

	// emitter is the metric emitter
	emitter metrics.MetricEmitter
}

func NewNPDFetcher(clientSet *client.GenericClientSet,
	cncFetcher cnc.CNCFetcher,
	conf *pkgconfig.Configuration,
	emitter metrics.MetricEmitter,
) (NPDFetcher, error) {
	checkpointManager, err := checkpointmanager.NewCheckpointManager(conf.CheckpointManagerDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}

	configLoader := kcc.NewKatalystCustomConfigLoader(clientSet, conf.ConfigCacheTTL, cncFetcher)
	return &npdFetcher{
		configLoader:        configLoader,
		checkpointManager:   checkpointManager,
		checkpointGraceTime: conf.ConfigCheckpointGraceTime,
		emitter:             emitter,
	}, nil
}

func NewDummyNPDFetcher() *DummyNPDFetcher {
	return &DummyNPDFetcher{}
}

func (f *npdFetcher) GetNPD(ctx context.Context) (*v1alpha1.NodeProfileDescriptor, error) {
	latestNpd, err := f.getNPD(ctx)
	if err != nil || latestNpd == nil {
		klog.Errorf("[npd-fetcher] try get new npd error: %v", err)
		// load from checkpoint if fetcher fails
		data, err := f.readCheckpoint()
		if err != nil {
			_ = f.emitter.StoreInt64(metricsNameUpdateNPD, 1, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: "status", Val: metricsValueStatusCheckpointNotFoundOrCorrupted},
			}...)
			return nil, errors.Wrap(err, "failed to load npd checkpoint")
		}
		npdData, timestamp := data.GetProfile()
		if time.Now().Before(timestamp.Add(f.checkpointGraceTime)) && npdData != nil {
			latestNpd = npdData
			klog.Infof("[npd-fetcher] failed to load npd from remote, use local checkpoint instead")
			_ = f.emitter.StoreInt64(metricsNameLoadNPDCheckpoint, 1, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: "status", Val: metricsValueStatusCheckpointSuccess},
				{Key: "npd", Val: latestNpd.Name},
			}...)
		} else {
			_ = f.emitter.StoreInt64(metricsNameLoadNPDCheckpoint, 1, metrics.MetricTypeNameRaw, []metrics.MetricTag{
				{Key: "status", Val: metricsValueStatusCheckpointInvalidOrExpired},
				{Key: "npd", Val: "invalid"},
			}...)
			return nil, fmt.Errorf("failed to load npd checkpoint, checkpoint expired or invalid")
		}
	}
	f.writeCheckpoint(latestNpd)
	return latestNpd, nil
}

// getNPD retrieves the latest NodeProfileDescriptor from the config loader.
func (f *npdFetcher) getNPD(ctx context.Context) (*v1alpha1.NodeProfileDescriptor, error) {
	npd := &v1alpha1.NodeProfileDescriptor{}
	if err := f.configLoader.LoadConfig(ctx, util.NPDGVR, npd); err != nil {
		return nil, err
	}
	return npd, nil
}

// readCheckpoint reads the checkpoint from disk
func (f *npdFetcher) readCheckpoint() (NodeProfileCheckpoint, error) {
	cp := NewCheckpoint(NodeProfileData{})
	if err := f.checkpointManager.GetCheckpoint(npdFetcherCheckpoint, cp); err != nil {
		klog.Errorf("[npd-fetcher] failed to get npd fetcher checkpoint: %v", err)
		return nil, err
	}
	return cp, nil
}

// writeCheckpoint writes the checkpoint to disk
func (f *npdFetcher) writeCheckpoint(npd *v1alpha1.NodeProfileDescriptor) {
	data := NewCheckpoint(NodeProfileData{})
	// set config value and timestamp for kind
	data.SetProfile(npd, metav1.Now())
	err := f.checkpointManager.CreateCheckpoint(npdFetcherCheckpoint, data)
	if err != nil {
		klog.Errorf("[npd-fetcher] failed to write checkpoint file %q: %v", npdFetcherCheckpoint, err)
	}
}
