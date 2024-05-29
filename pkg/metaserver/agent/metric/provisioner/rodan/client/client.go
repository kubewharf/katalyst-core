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

package client

import (
	"fmt"
	"io"
	"net/http"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/rodan/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

type RodanClient struct {
	urls map[string]string

	fetcher pod.PodFetcher

	metricFunc MetricFunc
}

func NewRodanClient(fetcher pod.PodFetcher, metricFunc MetricFunc, port int) *RodanClient {
	urls := make(map[string]string)
	for path := range types.MetricsMap {
		urls[path] = fmt.Sprintf("http://localhost:%d%s", port, path)
	}

	if metricFunc == nil {
		metricFunc = getMetrics
	}

	return &RodanClient{
		fetcher:    fetcher,
		urls:       urls,
		metricFunc: metricFunc,
	}
}

type MetricFunc func(url string, params map[string]string) ([]byte, error)

func getMetrics(url string, params map[string]string) ([]byte, error) {
	if len(params) != 0 {
		firstParam := true

		for k, v := range params {
			if firstParam {
				url += "?"
				firstParam = false
			} else {
				url += "&"
			}
			url = fmt.Sprintf("%s%s=%s", url, k, v)
		}
	}

	rsp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to get metrics, url: %v, err: %v", url, err)
	}
	defer func() { _ = rsp.Body.Close() }()

	if rsp.StatusCode != 200 {
		return nil, fmt.Errorf("invalid http response status code %d, status: %s, url: %s", rsp.StatusCode, rsp.Status, url)
	}

	return io.ReadAll(rsp.Body)
}
