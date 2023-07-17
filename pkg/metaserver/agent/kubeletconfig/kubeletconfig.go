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
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
)

// KubeletConfigFetcher is used to get the configuration of kubelet.
type KubeletConfigFetcher interface {
	// GetKubeletConfig returns the configuration of kubelet.
	GetKubeletConfig(ctx context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error)
}

// NewKubeletConfigFetcher returns a KubeletConfigFetcher
func NewKubeletConfigFetcher(conf *config.Configuration, emitter metrics.MetricEmitter) KubeletConfigFetcher {
	return &kubeletConfigFetcherImpl{
		emitter: emitter,
		conf:    conf,
	}
}

// kubeletConfigFetcherImpl use kubelet 10255 pods interface to get pod directly without cache.
type kubeletConfigFetcherImpl struct {
	emitter metrics.MetricEmitter
	conf    *config.Configuration
}

// GetKubeletConfig gets kubelet config from kubelet 10250/configz api
func (k *kubeletConfigFetcherImpl) GetKubeletConfig(ctx context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error) {
	if !k.conf.EnableKubeletSecurePort {
		return nil, fmt.Errorf("it is not enabled to get contents from kubelet secure port")
	}

	type configzWrapper struct {
		ComponentConfig kubeletconfigv1beta1.KubeletConfiguration `json:"kubeletconfig"`
	}
	configz := configzWrapper{}

	if err := process.GetAndUnmarshalForHttps(ctx, k.conf.KubeletSecurePort, k.conf.KubeletConfigURI, k.conf.APIAuthTokenFile, &configz); err != nil {
		return nil, fmt.Errorf("failed to get kubelet config, error: %v", err)
	}

	return &configz.ComponentConfig, nil
}
