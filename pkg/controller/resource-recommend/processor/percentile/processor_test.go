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
	"context"
	"sync"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

func TestProcessor_Register(t *testing.T) {
	tests := []struct {
		name          string
		processConfig *processortypes.ProcessConfig
		wantCErr      *errortypes.CustomError
	}{
		{
			name: "validate err",
			processConfig: &processortypes.ProcessConfig{
				ProcessKey: processortypes.ProcessKey{
					ResourceRecommendNamespacedName: types.NamespacedName{
						Namespace: "ns1",
						Name:      "n1",
					},
				},
			},
			wantCErr: errortypes.RegisterProcessTaskValidateError(errors.New("")),
		},
		{
			name:          "loaded",
			processConfig: &mockProcessConfig1,
			wantCErr:      nil,
		},
		{
			name: "case1",
			processConfig: &processortypes.ProcessConfig{
				ProcessKey: processortypes.ProcessKey{
					ResourceRecommendNamespacedName: types.NamespacedName{
						Namespace: "testRegisterNamespace1",
						Name:      "testRegisterName1",
					},
					Metric: &datasourcetypes.Metric{
						Namespace:     "testRegister",
						Kind:          "deployment",
						APIVersion:    "v1",
						WorkloadName:  "testRegister",
						ContainerName: "testRegister",
						Resource:      "cpu",
					},
				},
			},
			wantCErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCErr := mockProcessor.Register(tt.processConfig)
			if gotCErr != nil {
				if gotCErr.Phase != tt.wantCErr.Phase || gotCErr.Code != tt.wantCErr.Code {
					t.Errorf("Register() = %v, want %v", gotCErr, tt.wantCErr)
				}
				return
			}
			tasks, ok := mockProcessor.ResourceRecommendTaskIDsMap[tt.processConfig.ResourceRecommendNamespacedName]
			if !ok {
				t.Errorf("Register() failed, namespaceName not exist")
				return
			}
			id := (*tasks)[*tt.processConfig.Metric]
			if id != tt.processConfig.GenerateTaskID() {
				t.Errorf("Register() failed, Metric not exist")
				return
			}

			if _, ok := mockProcessor.AggregateTasks.Load(id); !ok {
				t.Errorf("Register() failed, task not store")
				return
			}
		})
	}
}

func TestProcessor_Cancel(t *testing.T) {
	processor := Processor{
		TaskQueue:                   workqueue.NewNamedRateLimitingQueue(DefaultQueueRateLimiter, ProcessorName),
		AggregateTasks:              &sync.Map{},
		ResourceRecommendTaskIDsMap: make(map[types.NamespacedName]*map[datasourcetypes.Metric]processortypes.TaskID),
	}

	ns0 := types.NamespacedName{Namespace: "CancelNamespace0", Name: "CancelName0"}
	ns1 := types.NamespacedName{Namespace: "CancelNamespace1", Name: "CancelName1"}
	ns2 := types.NamespacedName{Namespace: "CancelNamespace2", Name: "CancelName2"}
	ns3 := types.NamespacedName{Namespace: "CancelNamespace3", Name: "CancelName3"}
	notFoundNs := types.NamespacedName{Namespace: "NamespacedNameNotFound", Name: "NamespacedNameNotFound"}

	pk0 := processortypes.ProcessKey{
		ResourceRecommendNamespacedName: ns0,
		Metric: &datasourcetypes.Metric{
			Namespace:     "testCancel",
			Kind:          "deployment",
			APIVersion:    "v1",
			WorkloadName:  "testCancel0",
			ContainerName: "testCancel0-1",
			Resource:      v1.ResourceCPU,
		},
	}
	pk1 := processortypes.ProcessKey{
		ResourceRecommendNamespacedName: ns1,
		Metric: &datasourcetypes.Metric{
			Namespace:     "testCancel1",
			Kind:          "deployment",
			APIVersion:    "v1",
			WorkloadName:  "testCancel1",
			ContainerName: "testCancel1-1",
			Resource:      v1.ResourceCPU,
		},
	}
	pk2 := processortypes.ProcessKey{
		ResourceRecommendNamespacedName: ns1,
		Metric: &datasourcetypes.Metric{
			Namespace:     "testCancel1",
			Kind:          "deployment",
			APIVersion:    "v1",
			WorkloadName:  "testCancel1",
			ContainerName: "testCancel1-1",
			Resource:      v1.ResourceMemory,
		},
	}
	pk3 := processortypes.ProcessKey{
		ResourceRecommendNamespacedName: ns2,
		Metric: &datasourcetypes.Metric{
			Namespace:     "testCancel",
			Kind:          "deployment",
			APIVersion:    "v1",
			WorkloadName:  "testCancel2",
			ContainerName: "testCancel2-1",
			Resource:      v1.ResourceMemory,
		},
	}
	pk4 := processortypes.ProcessKey{
		ResourceRecommendNamespacedName: ns2,
		Metric: &datasourcetypes.Metric{
			Namespace:     "testCancel",
			Kind:          "deployment",
			APIVersion:    "v1",
			WorkloadName:  "testCancel2",
			ContainerName: "testCancel2-2",
			Resource:      v1.ResourceMemory,
		},
	}
	pk5 := processortypes.ProcessKey{
		ResourceRecommendNamespacedName: ns3,
		Metric: &datasourcetypes.Metric{
			Namespace:     "testCancel",
			Kind:          "deployment",
			APIVersion:    "v1",
			WorkloadName:  "testCancel3",
			ContainerName: "testCancel3-1",
			Resource:      v1.ResourceMemory,
		},
	}
	processConfig0 := processortypes.ProcessConfig{ProcessKey: pk0}
	processConfig1 := processortypes.ProcessConfig{ProcessKey: pk1}
	processConfig2 := processortypes.ProcessConfig{ProcessKey: pk2}
	processConfig3 := processortypes.ProcessConfig{ProcessKey: pk3}
	processConfig4 := processortypes.ProcessConfig{ProcessKey: pk4}
	processConfig5 := processortypes.ProcessConfig{ProcessKey: pk5}

	_ = processor.Register(&processConfig0)
	_ = processor.Register(&processConfig1)
	_ = processor.Register(&processConfig2)
	_ = processor.Register(&processConfig3)
	_ = processor.Register(&processConfig4)
	_ = processor.Register(&processConfig5)

	type want struct {
		existTaskIDs       []processortypes.TaskID
		notExistTaskIDs    []processortypes.TaskID
		namespaceTaskIsNil bool
	}
	tests := []struct {
		name           string
		testProcessKey *processortypes.ProcessKey
		wantCErr       *errortypes.CustomError
		want           want
	}{
		{
			name:           "case1",
			testProcessKey: nil,
			wantCErr:       nil,
		},
		{
			name:           "NamespacedName not found",
			testProcessKey: &processortypes.ProcessKey{ResourceRecommendNamespacedName: notFoundNs},
			wantCErr:       errortypes.NotFoundTasksError(notFoundNs),
		},
		{
			name: "metric_not_found",
			testProcessKey: &processortypes.ProcessKey{
				ResourceRecommendNamespacedName: ns0,
				Metric:                          &datasourcetypes.Metric{WorkloadName: "metricNotFound"},
			},
			want: want{
				existTaskIDs:       []processortypes.TaskID{processConfig0.GenerateTaskID()},
				notExistTaskIDs:    []processortypes.TaskID{},
				namespaceTaskIsNil: false,
			},
		},
		{
			name:           "delete_task",
			testProcessKey: &pk1,
			wantCErr:       nil,
			want: want{
				existTaskIDs:       []processortypes.TaskID{processConfig2.GenerateTaskID()},
				notExistTaskIDs:    []processortypes.TaskID{processConfig1.GenerateTaskID()},
				namespaceTaskIsNil: false,
			},
		},
		{
			name:           "delete_all_task_belong_to_NamespacedName",
			testProcessKey: &processortypes.ProcessKey{ResourceRecommendNamespacedName: ns2},
			wantCErr:       nil,
			want: want{
				existTaskIDs:       []processortypes.TaskID{},
				notExistTaskIDs:    []processortypes.TaskID{processConfig3.GenerateTaskID(), processConfig4.GenerateTaskID()},
				namespaceTaskIsNil: true,
			},
		},
		{
			name:           "delete_task_and_NamespacedName_in_map",
			testProcessKey: &pk5,
			wantCErr:       nil,
			want: want{
				existTaskIDs:       []processortypes.TaskID{},
				notExistTaskIDs:    []processortypes.TaskID{processConfig5.GenerateTaskID()},
				namespaceTaskIsNil: true,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCErr := processor.Cancel(tt.testProcessKey)
			if gotCErr != nil {
				if gotCErr.Phase != tt.wantCErr.Phase || gotCErr.Code != tt.wantCErr.Code {
					t.Errorf("Cancel() = %v, want %v", gotCErr, tt.wantCErr)
				}
				return
			}
			if tt.testProcessKey == nil && tt.wantCErr == nil {
				return
			}
			tasks, ok := processor.ResourceRecommendTaskIDsMap[tt.testProcessKey.ResourceRecommendNamespacedName]
			if tt.want.namespaceTaskIsNil || tt.testProcessKey.Metric == nil {
				if ok {
					t.Errorf("Cancel() failed, tasks for namespaceName(%s) exist", tt.testProcessKey.ResourceRecommendNamespacedName)
					return
				}
			} else {
				if !ok || tasks == nil {
					t.Errorf("Cancel() failed, tasks for namespaceName(%s) not exist", tt.testProcessKey.ResourceRecommendNamespacedName)
					return
				}
				if _, ok := (*tasks)[*tt.testProcessKey.Metric]; ok {
					t.Errorf("Cancel() failed, task for Metric(%s) exist", *tt.testProcessKey.Metric)
				}
			}
			for _, id := range tt.want.notExistTaskIDs {
				if _, ok := processor.AggregateTasks.Load(id); ok {
					t.Errorf("Cancel() failed, task for taskID(%s) exist", id)
				}
			}
			for _, id := range tt.want.existTaskIDs {
				if _, ok := processor.AggregateTasks.Load(id); !ok {
					t.Errorf("Cancel() failed, task for taskID(%s) not exist", id)
				}
			}
		})
	}
}

func TestProcessor_Run(t *testing.T) {
	processor := Processor{
		TaskQueue:                   workqueue.NewNamedRateLimitingQueue(DefaultQueueRateLimiter, ProcessorName),
		AggregateTasks:              &sync.Map{},
		ResourceRecommendTaskIDsMap: make(map[types.NamespacedName]*map[datasourcetypes.Metric]processortypes.TaskID),
	}
	type args struct {
		ctx context.Context
	}

	tests := []struct {
		name string
		args args
	}{
		{
			name: "case",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx1, cancelFunc := context.WithCancel(context.Background())
			go processor.Run(ctx1)
			time.Sleep(2 * time.Second)
			cancelFunc()
		})
	}
}

func TestProcessor_QueryProcessedValues(t *testing.T) {
	tests := []struct {
		name       string
		processKey *processortypes.ProcessKey
		want       float64
		wantErr    bool
	}{
		{
			name:       "case1",
			processKey: &metricIsNilProcessorKey,
			wantErr:    true,
		},
		{
			name:       "case2",
			processKey: &emptyTaskProcessorKey,
			wantErr:    true,
		},
		{
			name:       "case3",
			processKey: &mockProcessKey1,
			wantErr:    false,
			want:       mockTask195PercentileValue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mockProcessor.QueryProcessedValues(tt.processKey)
			if (err != nil) != tt.wantErr {
				t.Errorf("QueryProcessedValues() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				assert.InEpsilon(t, tt.want, got, 1e-5)
			}
		})
	}
}
