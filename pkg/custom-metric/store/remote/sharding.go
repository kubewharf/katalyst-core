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

package remote

import (
	"context"
	"fmt"
	"net/http"

	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	metricconf "github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
	sd "github.com/kubewharf/katalyst-core/pkg/util/service-discovery"
)

const httpMetricURL = "http://%v"

// ShardingController is responsible to separate the metric store into
// several sharding pieces to tolerant single node failure, as well as
// avoiding memory pressure in single node.
//
// todo: currently, it not really a valid
type ShardingController struct {
	ctx context.Context

	sdManager  sd.ServiceDiscoveryManager
	totalCount int
}

func NewShardingController(ctx context.Context, baseCtx *katalystbase.GenericContext,
	storeConf *metricconf.StoreConfiguration,
) (*ShardingController, error) {
	sdManager, err := sd.GetSDManager(ctx, baseCtx, storeConf.ServiceDiscoveryConf)
	if err != nil {
		return nil, err
	}

	// since collector will define its own pod/node label selectors, so we will construct informer separately
	s := &ShardingController{
		ctx:        ctx,
		totalCount: storeConf.StoreServerReplicaTotal,
		sdManager:  sdManager,
	}

	return s, nil
}

func (s *ShardingController) Start() error {
	return s.sdManager.Run()
}

func (s *ShardingController) Stop() error {
	return nil
}

// GetRWCount returns the quorum read/write counts
func (s *ShardingController) GetRWCount() (int, int) {
	r := (s.totalCount + 1) / 2
	w := s.totalCount - r + 1
	return r, w
}

// GetRequests returns the pre-generated http requests
func (s *ShardingController) GetRequests(ctx context.Context, path string) ([]*http.Request, error) {
	endpoints, err := s.sdManager.GetEndpoints()
	if err != nil {
		return nil, fmt.Errorf("failed get endpoints from serviceDiscoveryManager: %v", err)
	}
	klog.V(6).Infof("%v current endpoints is %v", s.sdManager.Name(), endpoints)

	requests := make([]*http.Request, 0, len(endpoints))
	for _, endpoint := range endpoints {
		req, err := s.generateRequest(ctx, endpoint, path)
		if err != nil {
			klog.Errorf("failed to generate request err: %v", err)
			continue
		}
		requests = append(requests, req)
	}

	return requests, nil
}

func (s *ShardingController) generateRequest(ctx context.Context, endpoint, path string) (*http.Request, error) {
	url := fmt.Sprintf(httpMetricURL, endpoint+path)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("new http request for %v err: %v", url, err)
	}

	req.Header.Add("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "remote-store")

	switch path {
	case local.ServingGetPath:
		req.Method = "GET"
	case local.ServingSetPath:
		req.Method = "POST"
	case local.ServingListPath:
		req.Method = "GET"
	}

	return req, nil
}
