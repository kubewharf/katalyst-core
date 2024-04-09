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

package metacache

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"

	borweinconsts "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/inference/models/borwein/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestMetaCacheImp_GetFilteredInferenceResult(t *testing.T) {
	t.Parallel()
	type fields struct {
		emitter       metrics.MetricEmitter
		modelToResult map[string]interface{}
	}
	type args struct {
		filterFunc func(result interface{}) (interface{}, error)
		modelName  string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    interface{}
		wantErr bool
	}{
		{
			name: "test without filter",
			fields: fields{
				emitter: metrics.DummyMetrics{},
				modelToResult: map[string]interface{}{
					borweinconsts.ModelNameBorwein: []int{1, 2, 3},
				},
			},
			args: args{
				modelName: borweinconsts.ModelNameBorwein,
			},
			want:    []int{1, 2, 3},
			wantErr: false,
		},
		{
			name: "test with filter",
			fields: fields{
				emitter: metrics.DummyMetrics{},
				modelToResult: map[string]interface{}{
					borweinconsts.ModelNameBorwein: []int{1, 2, 3},
				},
			},
			args: args{
				filterFunc: func(result interface{}) (interface{}, error) {
					parsedResult, ok := result.([]int)

					if !ok {
						return nil, fmt.Errorf("invalid result")
					}

					filteredResult := []int{}

					for _, result := range parsedResult {
						if result < 3 {
							filteredResult = append(filteredResult, result)
						}
					}

					return filteredResult, nil
				},
				modelName: borweinconsts.ModelNameBorwein,
			},
			want:    []int{1, 2},
			wantErr: false,
		},
		{
			name: "test with invalid result type",
			fields: fields{
				emitter: metrics.DummyMetrics{},
				modelToResult: map[string]interface{}{
					borweinconsts.ModelNameBorwein: []string{"1", "2", "3"},
				},
			},
			args: args{
				filterFunc: func(result interface{}) (interface{}, error) {
					parsedResult, ok := result.([]int)

					if !ok {
						return nil, fmt.Errorf("invalid result")
					}

					filteredResult := []int{}

					for _, result := range parsedResult {
						if result < 3 {
							filteredResult = append(filteredResult, result)
						}
					}

					return filteredResult, nil
				},
				modelName: borweinconsts.ModelNameBorwein,
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &MetaCacheImp{
				emitter:       tt.fields.emitter,
				modelToResult: tt.fields.modelToResult,
			}
			got, err := mc.GetFilteredInferenceResult(tt.args.filterFunc, tt.args.modelName)
			if (err != nil) != tt.wantErr {
				t.Errorf("MetaCacheImp.GetFilteredInferenceResult() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MetaCacheImp.GetFilteredInferenceResult() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMetaCacheImp_GetInferenceResult(t *testing.T) {
	t.Parallel()
	type fields struct {
		emitter       metrics.MetricEmitter
		modelToResult map[string]interface{}
	}
	type args struct {
		modelName string
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    interface{}
		wantErr bool
	}{
		{
			name: "test get result directly",
			fields: fields{
				emitter: metrics.DummyMetrics{},
				modelToResult: map[string]interface{}{
					borweinconsts.ModelNameBorwein: []int{1, 2, 3},
				},
			},
			args: args{
				modelName: borweinconsts.ModelNameBorwein,
			},
			want:    []int{1, 2, 3},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &MetaCacheImp{
				emitter:       tt.fields.emitter,
				modelToResult: tt.fields.modelToResult,
			}
			got, err := mc.GetInferenceResult(tt.args.modelName)
			if (err != nil) != tt.wantErr {
				t.Errorf("MetaCacheImp.GetInferenceResult() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MetaCacheImp.GetInferenceResult() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMetaCacheImp_SetInferenceResult(t *testing.T) {
	t.Parallel()
	type fields struct {
		emitter       metrics.MetricEmitter
		modelToResult map[string]interface{}
	}
	type args struct {
		modelName string
		result    interface{}
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		want    interface{}
		wantErr bool
	}{
		{
			name: "get after set",
			fields: fields{
				emitter:       metrics.DummyMetrics{},
				modelToResult: make(map[string]interface{}),
			},
			args: args{
				modelName: borweinconsts.ModelNameBorwein,
				result:    []int{1, 2, 3},
			},
			want:    []int{1, 2, 3},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mc := &MetaCacheImp{
				emitter:       tt.fields.emitter,
				modelToResult: tt.fields.modelToResult,
			}

			if err := mc.SetInferenceResult(tt.args.modelName, tt.args.result); (err != nil) != tt.wantErr {
				t.Errorf("MetaCacheImp.SetInferenceResult() error = %v, wantErr %v", err, tt.wantErr)
			}

			got, err := mc.GetInferenceResult(tt.args.modelName)
			if (err != nil) != tt.wantErr {
				t.Errorf("MetaCacheImp.GetInferenceResult() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("MetaCacheImp.GetInferenceResult() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRangeAndDeleteContainerWithSafeTime(t *testing.T) {
	testDir := "/tmp/mc-test-range-delete"
	checkpointManager, err := checkpointmanager.NewCheckpointManager(testDir)
	require.NoError(t, err, "failed to create checkpoint manager")
	defer func() {
		os.RemoveAll(testDir)
	}()

	mc := &MetaCacheImp{
		podEntries:               map[string]types.ContainerEntries{},
		containerCreateTimestamp: map[string]int64{},
		checkpointManager:        checkpointManager,
		emitter:                  metrics.DummyMetrics{},
		checkpointName:           "test-mc-range-delete",
	}
	ci := &types.ContainerInfo{
		PodUID:        "pod1",
		ContainerName: "c1",
	}
	require.NoError(t, mc.AddContainer("pod1", "c1", ci), "failed to add container")

	require.NoError(t, mc.RangeAndDeleteContainer(func(containerInfo *types.ContainerInfo) bool {
		return true
	}, 0), "failed to range and delete container without safe time")
	require.Equal(t, 0, len(mc.podEntries), "failed to delete container without safe time")
	require.Equal(t, 0, len(mc.containerCreateTimestamp), "failed to delete container create timestamp without safe time")

	require.NoError(t, mc.AddContainer("pod1", "c1", ci), "failed to add container")
	require.NoError(t, mc.RangeAndDeleteContainer(func(containerInfo *types.ContainerInfo) bool {
		return true
	}, 1), "failed to skip range and delete container with safe time")
	require.Equal(t, 1, len(mc.podEntries), "failed to protect container with safe time")
	require.Equal(t, 1, len(mc.containerCreateTimestamp), "failed to protect container create timestamp with safe time")

	require.NoError(t, mc.RangeAndDeleteContainer(func(containerInfo *types.ContainerInfo) bool {
		return true
	}, time.Now().UnixNano()), "failed to skip range and delete container with safe time")
	require.Equal(t, 0, len(mc.podEntries), "failed to delete container before safe time")
	require.Equal(t, 0, len(mc.containerCreateTimestamp), "failed to delete container create timestamp before safe time")
}
