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

func TestParseTHPDefragPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		want    apiconfig.THPDefragPolicy
		wantErr bool
	}{
		{name: "always", input: "always", want: apiconfig.THPDefragPolicyAlways},
		{name: "defer", input: "defer", want: apiconfig.THPDefragPolicyDefer},
		{name: "defer madvise", input: "defer+madvise", want: apiconfig.THPDefragPolicyDeferMadvise},
		{name: "madvise", input: "madvise", want: apiconfig.THPDefragPolicyMadvise},
		{name: "never", input: "never", want: apiconfig.THPDefragPolicyNever},
		{name: "normalize", input: " Defer+Madvise \n", want: apiconfig.THPDefragPolicyDeferMadvise},
		{name: "empty leaves policy unmanaged"},
		{name: "whitespace leaves policy unmanaged", input: " \t\n"},
		{name: "reject shmem force", input: "force", wantErr: true},
		{name: "reject shmem deny", input: "deny", wantErr: true},
		{name: "reject shmem advise", input: "advise", wantErr: true},
		{name: "reject shmem within_size", input: "within_size", wantErr: true},
		{name: "reject unknown policy", input: "invalid", wantErr: true},
		{name: "reject internal spaces", input: "defer + madvise", wantErr: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseTHPDefragPolicy(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), "invalid THP defrag policy")
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFragMemConfigurationApplyConfiguration(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	enable := true
	score := int64(70)
	mode := "always"
	threshold := int64(90)
	staticEnable := true
	staticTHP := apiconfig.THPModeAlways
	staticTHPShm := apiconfig.THPShmModeForce
	defragPolicy := apiconfig.THPDefragPolicyNever

	conf := NewFragMemConfiguration()
	conf.ApplyConfiguration(&crd.DynamicConfigCRD{
		AdminQoSConfiguration: &apiconfig.AdminQoSConfiguration{
			Spec: apiconfig.AdminQoSConfigurationSpec{
				Config: apiconfig.AdminQoSConfig{
					QRMPluginConfig: &apiconfig.QRMPluginConfig{
						MemoryPluginConfig: &apiconfig.MemoryPluginConfig{
							FragMemConfig: &apiconfig.FragMemConfig{
								EnableFragMem:     &enable,
								MemFragScoreAsync: &score,
								THPDefaultConfig:  &mode,
								THPDefragPolicy:   &defragPolicy,
								THPStaticEnableConfig: &apiconfig.THPStaticEnableConfig{
									Enable: &staticEnable,
									THP:    &staticTHP,
									THPShm: &staticTHPShm,
								},
								THPHighOrderScoreThreshold: &threshold,
							},
						},
					},
				},
			},
		},
	})

	as.True(conf.EnableFragMem)
	as.Equal(70, conf.MemFragScoreAsync)
	as.Equal(apiconfig.THPModeAlways, conf.THPDefaultConfig)
	as.Equal(90, conf.THPHighOrderScoreThreshold)
	as.Equal(apiconfig.THPDefragPolicyNever, conf.THPDefragPolicy)
	as.NotNil(conf.THPStaticEnableConfig)
	as.True(conf.THPStaticEnableConfig.Enable)
	as.Equal(apiconfig.THPModeAlways, conf.THPStaticEnableConfig.THP)
	as.Equal(apiconfig.THPShmModeForce, conf.THPStaticEnableConfig.THPShm)
}
