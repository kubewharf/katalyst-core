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

package processor

import (
	"fmt"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	datasourcetype "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
)

func TestProcessConfig_GenerateTaskID(t *testing.T) {
	type fields struct {
		namespacedName     types.NamespacedName
		targetRef          v1alpha1.CrossVersionObjectReference
		containerName      string
		controlledResource v1.ResourceName
		taskConfig         TaskConfigStr
	}
	tests := []struct {
		name   string
		fields fields
		want   TaskID
	}{
		{
			name: "case1",
			fields: fields{
				namespacedName: types.NamespacedName{
					Name:      "recommendation1",
					Namespace: "default",
				},
				targetRef: v1alpha1.CrossVersionObjectReference{
					Kind:       "Deployment",
					Name:       "demo",
					APIVersion: "app/v1",
				},
				containerName:      "c1",
				controlledResource: "cpu",
				taskConfig:         "",
			},
			want: "default/recommendation1-Deployment-app/v1-demo-c1-cpu-",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc := NewProcessConfig(tt.fields.namespacedName, tt.fields.targetRef, tt.fields.containerName, tt.fields.controlledResource, tt.fields.taskConfig)
			if got := pc.GenerateTaskID(); got != tt.want {
				t.Errorf("Validate() error, got: %s, want: %s", got, tt.want)
			}
		})
	}
}

func TestProcessConfig_Validate(t *testing.T) {
	tests := []struct {
		name          string
		processConfig ProcessConfig
		wantErr       error
	}{
		{
			name:          "metric is empty",
			processConfig: ProcessConfig{},
			wantErr:       fmt.Errorf("metric is empty"),
		},
		{
			name: "containerName is empty",
			processConfig: ProcessConfig{
				ProcessKey: ProcessKey{
					Metric: &datasourcetype.Metric{},
				},
			},
			wantErr: fmt.Errorf("containerName is empty"),
		},
		{
			name: "kind is empty",
			processConfig: ProcessConfig{
				ProcessKey: ProcessKey{
					Metric: &datasourcetype.Metric{
						ContainerName: "c1",
					},
				},
			},
			wantErr: fmt.Errorf("kind is empty"),
		},
		{
			name: "workloadName is empty",
			processConfig: ProcessConfig{
				ProcessKey: ProcessKey{
					Metric: &datasourcetype.Metric{
						ContainerName: "c1",
						Kind:          "Deployment",
					},
				},
			},
			wantErr: fmt.Errorf("workloadName is empty"),
		},
		{
			name: "controlledResource not support",
			processConfig: ProcessConfig{
				ProcessKey: ProcessKey{
					Metric: &datasourcetype.Metric{
						ContainerName: "c1",
						Kind:          "Deployment",
						WorkloadName:  "w1",
						Resource:      "Disk",
					},
				},
			},
			wantErr: fmt.Errorf("controlledResource only can be [%s, %s]", v1.ResourceCPU, v1.ResourceMemory),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.processConfig.Validate(); err.Error() != tt.wantErr.Error() {
				t.Errorf("Validate() error, gotErr: %v, wantErr: %v", err, tt.wantErr)
			}
		})
	}
}
