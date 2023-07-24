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

package cnc

import (
	"context"
	"fmt"
	"time"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	configv1alpha1 "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/typed/config/v1alpha1"
)

type fakeCNCFetcher struct{}

func NewFakeCNCFetcher(_ string, _ time.Duration, _ configv1alpha1.CustomNodeConfigInterface) CNCFetcher {
	return &fakeCNCFetcher{}
}

func (c *fakeCNCFetcher) GetCNC(_ context.Context) (*v1alpha1.CustomNodeConfig, error) {
	return nil, fmt.Errorf("cnc fetcher is not enabled")
}
