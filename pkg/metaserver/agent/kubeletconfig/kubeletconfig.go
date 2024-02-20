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

	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// KubeletConfigFetcher is used to get the configuration of kubelet.
type KubeletConfigFetcher interface {
	// GetKubeletConfig returns the configuration of kubelet.
	GetKubeletConfig(ctx context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error)
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

// GetKubeletConfig gets kubelet config from kubelet 10250/configz api
func (k *kubeletConfigFetcherImpl) GetKubeletConfig(ctx context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error) {
	if !k.baseConf.KubeletSecurePortEnabled {
		return nil, fmt.Errorf("it is not enabled to get contents from kubelet secure port")
	}

	type configzWrapper struct {
		ComponentConfig kubeletconfigv1beta1.KubeletConfiguration `json:"kubeletconfig"`
	}
	configz := configzWrapper{}

	if err := native.GetAndUnmarshalForHttps(ctx, k.baseConf.KubeletSecurePort, k.baseConf.NodeAddress,
		k.baseConf.KubeletConfigEndpoint, k.baseConf.APIAuthTokenFile, &configz); err != nil {
		return nil, fmt.Errorf("failed to get kubelet config, error: %v", err)
	}

	return &configz.ComponentConfig, nil
}
