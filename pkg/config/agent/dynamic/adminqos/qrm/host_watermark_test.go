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

package qrm

import (
	"testing"

	"github.com/stretchr/testify/require"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
)

func TestHostWatermarkConfigurationApplyConfiguration(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	enable := true
	scale := int64(1234)
	boost := int64(99)
	extfrag := int64(500)
	reserved := int64(10)

	conf := NewHostWatermarkConfiguration()
	conf.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &apiconfig.AdminQoSConfiguration{
			Spec: apiconfig.AdminQoSConfigurationSpec{
				Config: apiconfig.AdminQoSConfig{
					QRMPluginConfig: &apiconfig.QRMPluginConfig{
						MemoryPluginConfig: &apiconfig.MemoryPluginConfig{
							HostWatermarkConfig: &apiconfig.HostWatermarkConfig{
								EnableHostWatermark:       &enable,
								VMWatermarkScaleFactor:    &scale,
								VMWatermarkBoostFactor:    &boost,
								VMExtFragThreshold:        &extfrag,
								ReservedKswapdWatermarkGB: &reserved,
							},
						},
					},
				},
			},
		},
	})

	as.True(conf.EnableHostWatermark)
	as.Equal(1234, conf.VMWatermarkScaleFactor)
	as.Equal(99, conf.VMWatermarkBoostFactor)
	as.Equal(500, conf.VMExtFragThreshold)
	as.Equal(uint64(10), conf.ReservedKswapdWatermarkGB)
}
