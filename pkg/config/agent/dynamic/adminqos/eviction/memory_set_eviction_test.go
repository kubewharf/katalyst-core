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

package eviction

import (
	"testing"

	"github.com/stretchr/testify/require"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestMemorySetEvictionConfigurationApplyTo(t *testing.T) {
	t.Parallel()

	as := require.New(t)

	conf := NewMemorySetEvictionConfiguration()
	as.False(conf.EnableMemorySetEviction)

	conf.ApplyTo(&crd.DynamicConfigCRD{})
	as.False(conf.EnableMemorySetEviction)

	enabled := true
	conf.ApplyTo(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
			Spec: configapi.AdminQoSConfigurationSpec{
				Config: configapi.AdminQoSConfig{
					EvictionConfig: &configapi.EvictionConfig{
						MemorySetEvictionConfig: &configapi.MemorySetEvictionConfig{
							EnableMemorySetEviction: &enabled,
						},
					},
				},
			},
		},
	})
	as.True(conf.EnableMemorySetEviction)

	enabled = false
	conf.ApplyTo(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &configapi.AdminQoSConfiguration{
			Spec: configapi.AdminQoSConfigurationSpec{
				Config: configapi.AdminQoSConfig{
					EvictionConfig: &configapi.EvictionConfig{
						MemorySetEvictionConfig: &configapi.MemorySetEvictionConfig{
							EnableMemorySetEviction: &enabled,
						},
					},
				},
			},
		},
	})
	as.False(conf.EnableMemorySetEviction)
}
