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

package manager

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor"
	"github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/log"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

type mockProcessor struct {
	algorithm v1alpha1.Algorithm
	runMark   string
}

func (p *mockProcessor) Run(ctx context.Context) {
	time.Sleep(1 * time.Second)
	p.runMark = string(p.algorithm) + "processor" + "-run"
	log.InfoS(ctx, "mockProcessor Run", "algorithm", p.algorithm)
}

func (p *mockProcessor) Register(_ *processortypes.ProcessConfig) *errortypes.CustomError {
	return nil
}

func (p *mockProcessor) Cancel(_ *processortypes.ProcessKey) *errortypes.CustomError { return nil }

func (p *mockProcessor) QueryProcessedValues(_ *processortypes.ProcessKey) (float64, error) {
	return 0, nil
}

func TestManager_StartProcess(t *testing.T) {
	mockProcessor1 := mockProcessor{algorithm: "mockAlgorithm1"}
	mockProcessor2 := mockProcessor{algorithm: "mockAlgorithm2"}
	mockProcessor3 := mockProcessor{algorithm: "mockAlgorithm3"}
	tests := []struct {
		name       string
		processors []*mockProcessor
	}{
		{
			name:       "case1",
			processors: []*mockProcessor{&mockProcessor1, &mockProcessor2, &mockProcessor3},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &Manager{processors: map[v1alpha1.Algorithm]processor.Processor{}}
			for _, p := range tt.processors {
				manager.processors[p.algorithm] = p
			}
			manager.StartProcess(context.Background())
			for _, p := range tt.processors {
				if p.runMark != string(p.algorithm)+"processor"+"-run" {
					t.Errorf("StartProcess() failed, %s processor not run", p.algorithm)
				}
			}
		})
	}
}

func TestManager_GetProcessor(t *testing.T) {
	manager1 := NewManager(nil, nil)
	type args struct {
		algorithm v1alpha1.Algorithm
	}
	tests := []struct {
		name    string
		manager *Manager
		args    args
		want    processor.Processor
	}{
		{
			name:    "Unknown_Algorithm",
			manager: manager1,
			args: args{
				algorithm: "Unknown",
			},
			want: manager1.processors[v1alpha1.AlgorithmPercentile],
		},
		{
			name:    "get_AlgorithmPercentile_processor",
			manager: manager1,
			args: args{
				algorithm: v1alpha1.AlgorithmPercentile,
			},
			want: manager1.processors[v1alpha1.AlgorithmPercentile],
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.manager.GetProcessor(tt.args.algorithm)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("GetProcessor() = %v, want %v", got, tt.want)
			}
		})
	}
}
