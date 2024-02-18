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

package percentile

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/percentile/task"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

func TestProcessor_getTaskForProcessKey(t *testing.T) {
	type newProcessor func() *Processor
	tests := []struct {
		name         string
		newProcessor newProcessor
		processKey   *processortypes.ProcessKey
		want         *task.HistogramTask
		wantErr      bool
	}{
		{
			name:       "processConfig is nil",
			processKey: nil,
			want:       nil,
			wantErr:    true,
		},
		{
			name:       "processConfig.Metric is nil",
			processKey: &metricIsNilProcessorKey,
			want:       nil,
			wantErr:    true,
		},
		{
			name:       "not found NamespacedName",
			processKey: &notExistNamespacedNameProcessorKey,
			want:       nil,
			wantErr:    true,
		},
		{
			name:       "not found Metric",
			processKey: &notExistMetricProcessorKey,
			want:       nil,
			wantErr:    true,
		},
		{
			name:       "not found Task",
			processKey: &notFoundTaskProcessorKey,
			want:       nil,
			wantErr:    true,
		},
		{
			name:       "task type illegal",
			processKey: &valueTypeIllegalProcessorKey,
			want:       nil,
			wantErr:    true,
		},
		{
			name:       "success",
			processKey: &mockProcessKey1,
			want:       mockTask1,
			wantErr:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mockProcessor.getTaskForProcessKey(tt.processKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("getTaskForProcessKey() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getTaskForProcessKey() got = %v, want %v", got, tt.want)
			}
		})
	}
}
