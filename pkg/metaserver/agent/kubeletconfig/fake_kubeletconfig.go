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

	kubeletconfigv1beta1 "k8s.io/kubelet/config/v1beta1"
)

// NewFakeKubeletConfigFetcher returns a fakeKubeletConfigFetcherImpl.
func NewFakeKubeletConfigFetcher(kubeletConfig kubeletconfigv1beta1.KubeletConfiguration) KubeletConfigFetcher {
	return &fakeKubeletConfigFetcherImpl{
		kubeletConfig: kubeletConfig,
	}
}

// fakeKubeletConfigFetcherImpl returns a fake kubelet config.
type fakeKubeletConfigFetcherImpl struct {
	kubeletConfig kubeletconfigv1beta1.KubeletConfiguration
}

// GetKubeletConfig returns a fake kubelet config.
func (f *fakeKubeletConfigFetcherImpl) GetKubeletConfig(_ context.Context) (*kubeletconfigv1beta1.KubeletConfiguration, error) {
	return &f.kubeletConfig, nil
}
