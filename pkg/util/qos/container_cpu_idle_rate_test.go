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

package qos

import (
	"testing"

	"github.com/stretchr/testify/assert"

	katalystapiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
)

func TestGetPodContainerCPUIdleRateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		podAnnotations map[string]string
		wantFound      bool
		wantConfig     ContainerCPUIdleRateConfig
		wantErr        bool
	}{
		{
			name:           "empty annotations",
			podAnnotations: map[string]string{},
			wantFound:      false,
		},
		{
			name: "annotation not configured",
			podAnnotations: map[string]string{
				"other": "value",
			},
			wantFound: false,
		},
		{
			name: "valid config",
			podAnnotations: map[string]string{
				katalystapiconsts.PodAnnotationContainerCPUIdleRateKey: `{"hdfsfuse-sidecar": 50, "another-sidecar": 30}`,
			},
			wantFound: true,
			wantConfig: ContainerCPUIdleRateConfig{
				"hdfsfuse-sidecar": 50,
				"another-sidecar":  30,
			},
		},
		{
			name: "invalid json",
			podAnnotations: map[string]string{
				katalystapiconsts.PodAnnotationContainerCPUIdleRateKey: `{"hdfsfuse-sidecar": }`,
			},
			wantFound: true,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			found, cfg, err := GetPodContainerCPUIdleRateConfigFromAnnotation(tt.podAnnotations)
			assert.Equal(t, tt.wantFound, found)
			assert.Equal(t, tt.wantConfig, cfg)
			assert.Equal(t, tt.wantErr, err != nil)
		})
	}
}
