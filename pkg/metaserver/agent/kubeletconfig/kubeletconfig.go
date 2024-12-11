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

package kubeletconfig

import (
	"context"
	"fmt"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const configzApi = "http://localhost:%v/%v"

// KubeletConfigFetcher is used to get the configuration of kubelet.
type KubeletConfigFetcher interface {
	// GetKubeletConfig returns the configuration of kubelet.
	GetKubeletConfig(ctx context.Context) (*native.KubeletConfiguration, error)
}

// NewKubeletConfigFetcher returns a KubeletConfigFetcher
func NewKubeletConfigFetcher(baseConf *global.BaseConfiguration, emitter metrics.MetricEmitter) KubeletConfigFetcher {
	return &kubeletConfigFetcherImpl{
		emitter:  emitter,
		baseConf: baseConf,
	}
}

// kubeletConfigFetcherImpl use kubelet 10255 pods interface to get pod directly without cache.
type kubeletConfigFetcherImpl struct {
	emitter  metrics.MetricEmitter
	baseConf *global.BaseConfiguration
}

// GetKubeletConfig gets kubelet config from kubelet 10250/configz api when KubeletSecurePortEnabled is true; otherwise 10255/configz
func (k *kubeletConfigFetcherImpl) GetKubeletConfig(ctx context.Context) (*native.KubeletConfiguration, error) {
	type configzWrapper struct {
		ComponentConfig native.KubeletConfiguration `json:"kubeletconfig"`
	}
	configz := configzWrapper{}

	if k.baseConf.KubeletSecurePortEnabled {
		if err := native.GetAndUnmarshalForHttps(ctx, k.baseConf.KubeletSecurePort, k.baseConf.NodeAddress,
			k.baseConf.KubeletConfigEndpoint, k.baseConf.APIAuthTokenFile, &configz); err != nil {
			return nil, fmt.Errorf("failed to get kubelet config via secure port, error: %v", err)
		}
	} else {
		url := fmt.Sprintf(configzApi, k.baseConf.KubeletReadOnlyPort, k.baseConf.KubeletConfigEndpoint)
		if err := process.GetAndUnmarshal(url, &configz); err != nil {
			return nil, fmt.Errorf("failed to get kubelet config via insecure port, error: %v", err)
		}
	}

	return &configz.ComponentConfig, nil
}
