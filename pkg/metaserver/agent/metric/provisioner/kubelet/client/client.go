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
	"context"
	"fmt"

	statsapi "k8s.io/kubelet/pkg/apis/stats/v1alpha1"

	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

type KubeletSummaryClient struct {
	conf *config.Configuration
}

// http://127.0.0.1:10255
func NewKubeletSummaryClient(conf *config.Configuration) *KubeletSummaryClient {
	return &KubeletSummaryClient{
		conf: conf,
	}
}

func (c *KubeletSummaryClient) Summary(ctx context.Context) (*statsapi.Summary, error) {
	summary := &statsapi.Summary{}
	if c.conf.EnableKubeletSecurePort {
		if err := native.GetAndUnmarshalForHttps(ctx, c.conf.KubeletSecurePort, c.conf.NodeAddress, c.conf.KubeletPodsEndpoint, c.conf.APIAuthTokenFile, summary); err != nil {
			return nil, fmt.Errorf("failed to get kubelet config for summary api, error: %v", err)
		}
	} else {
		const podsApi = "http://localhost:10255/stats/summary"
		if err := process.GetAndUnmarshal(podsApi, summary); err != nil {
			return nil, fmt.Errorf("failed to get summary, error: %v", err)
		}
	}

	return summary, nil
}
