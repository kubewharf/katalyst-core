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
	"fmt"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/workqueue"

	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/percentile/task"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

func TestProcessor_processTask(t *testing.T) {
	taskQueue1 := workqueue.NewNamedRateLimitingQueue(DefaultQueueRateLimiter, ProcessorName)
	//processor1 := Processor{
	//	TaskQueue: taskQueue1,
	//}

	taskQueue2 := workqueue.NewNamedRateLimitingQueue(DefaultQueueRateLimiter, ProcessorName)
	//processor2 := Processor{
	//	TaskQueue: taskQueue2,
	//}
	taskIDTaskTypeIllegal := 3

	taskQueue3 := workqueue.NewNamedRateLimitingQueue(DefaultQueueRateLimiter, ProcessorName)
	//processor3 := Processor{
	//	TaskQueue:      taskQueue3,
	//	AggregateTasks: &sync.Map{},
	//}
	taskID3 := processortypes.TaskID("case3")

	type testFunc func()
	type checkFunc func()
	tests := []struct {
		name      string
		processor Processor
		testFunc
		isContinueToRun bool
		checkFunc
	}{
		{
			name: "queue_shutdown",
			processor: Processor{
				TaskQueue: taskQueue1,
			},
			testFunc: func() {
				taskQueue1.ShutDown()
			},
			isContinueToRun: false,
			checkFunc:       func() {},
		},
		{
			name: "task_type_illegal",
			processor: Processor{
				TaskQueue: taskQueue2,
			},
			testFunc: func() {
				taskQueue2.Add(taskIDTaskTypeIllegal)
			},
			isContinueToRun: true,
			checkFunc: func() {
				if taskQueue2.Len() != 0 {
					t.Errorf("task type illegal not forget")
				}
			},
		},
		{
			name: "task_type_illegal",
			processor: Processor{
				TaskQueue:      taskQueue3,
				AggregateTasks: &sync.Map{},
			},
			testFunc: func() {
				taskQueue3.Add(taskID3)
			},
			isContinueToRun: true,
			checkFunc: func() {
				if taskQueue3.Len() != 0 {
					t.Errorf("not found task not forget")
				}
			},
		},
	}
	for i := 0; i < len(tests); i++ {
		t.Run(tests[i].name, func(t *testing.T) {
			wg := &sync.WaitGroup{}
			wg.Add(2)
			go func() {
				defer wg.Done()
				isContinueToRun := tests[i].processor.processTask(NewContext())
				if tests[i].isContinueToRun != isContinueToRun {
					t.Errorf("processTask() isShutdown = %v, want %v\n", isContinueToRun, tests[i].isContinueToRun)
					return
				}
			}()
			go func() {
				defer wg.Done()
				tests[i].testFunc()
			}()
			wg.Wait()
			tests[i].checkFunc()
		})
	}
}

type MockDatasourceForProcessTasks struct{}

func (m1 *MockDatasourceForProcessTasks) QueryTimeSeries(_ *datasourcetypes.Query, _ time.Time, _ time.Time, _ time.Duration) (*datasourcetypes.TimeSeries, error) {
	return &datasourcetypes.TimeSeries{Samples: []datasourcetypes.Sample{
		{
			Timestamp: 1694270256,
			Value:     1,
		},
	}}, nil
}

func (m1 *MockDatasourceForProcessTasks) ConvertMetricToQuery(metric datasourcetypes.Metric) (*datasourcetypes.Query, error) {
	return nil, nil
}

func TestProcessor_processTask1(t *testing.T) {
	queueRateLimiter := workqueue.NewMaxOfRateLimiter(
		workqueue.NewItemExponentialFailureRateLimiter(time.Second, 32*time.Second),
		// 10 qps, 100 bucket size.  This is only for retry speed and its only the overall factor (not per item)
		&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
	)
	taskQueue1 := workqueue.NewNamedRateLimitingQueue(queueRateLimiter, ProcessorName)
	taskID1 := processortypes.TaskID("task_run_err")
	aggregateTasks1 := &sync.Map{}
	aggregateTasks1.Store(taskID1, &task.HistogramTask{})

	taskQueue2 := workqueue.NewNamedRateLimitingQueue(queueRateLimiter, ProcessorName)
	taskID := processortypes.TaskID("task_run_err")
	mockTask, _ := task.NewTask(datasourcetypes.Metric{Resource: v1.ResourceCPU}, "")
	mockTask.ProcessInterval = time.Second * 2
	aggregateTasks2 := &sync.Map{}
	aggregateTasks2.Store(taskID, mockTask)

	proxy := datasource.NewProxy()
	proxy.RegisterDatasource(datasource.PrometheusDatasource, &MockDatasourceForProcessTasks{})

	type testFunc func()
	type runFunc func()
	tests := []struct {
		name      string
		processor Processor
		testFunc
		runFunc
		runTimes      int
		wantRunSecond int64
	}{
		{
			name: "run_err",
			processor: Processor{
				TaskQueue:      taskQueue1,
				AggregateTasks: aggregateTasks1,
			},
			testFunc: func() {
				taskQueue1.Add(taskID1)
			},
			runTimes:      4,
			wantRunSecond: 7,
		},
		{
			name: "run",
			processor: Processor{
				TaskQueue:       taskQueue2,
				AggregateTasks:  aggregateTasks2,
				DatasourceProxy: proxy,
			},
			testFunc: func() {
				taskQueue2.Add(taskID)
			},
			runTimes:      3,
			wantRunSecond: 4,
		},
	}
	for i := 0; i < len(tests); i++ {
		t.Run(tests[i].name, func(t *testing.T) {
			wg := &sync.WaitGroup{}
			wg.Add(2)
			go func() {
				defer wg.Done()
				beginTime := time.Now()
				for j := 0; j < tests[i].runTimes; j++ {
					_ = tests[i].processor.processTask(NewContext())
				}
				runtime := int64(time.Now().Sub(beginTime) / time.Second)
				if runtime != tests[i].wantRunSecond {
					t.Errorf("processTask() runtime = %ds, want %ds", runtime, tests[i].wantRunSecond)
				}
			}()
			go func() {
				defer wg.Done()
				tests[i].testFunc()
			}()
			wg.Wait()
		})
	}
}

type MockDatasource1ForProcessTasks struct{}

func (m1 *MockDatasource1ForProcessTasks) QueryTimeSeries(_ *datasourcetypes.Query, _ time.Time, _ time.Time, _ time.Duration) (*datasourcetypes.TimeSeries, error) {
	time.Sleep(2 * time.Second)
	return &datasourcetypes.TimeSeries{Samples: []datasourcetypes.Sample{
		{
			Timestamp: 1694270256,
			Value:     1,
		},
	}}, nil
}

func (m1 *MockDatasource1ForProcessTasks) ConvertMetricToQuery(metric datasourcetypes.Metric) (*datasourcetypes.Query, error) {
	return nil, nil
}

func TestProcessor_ProcessTasks(t *testing.T) {
	q := workqueue.NewNamedRateLimitingQueue(DefaultQueueRateLimiter, ProcessorName)
	aggregateTasks := &sync.Map{}
	proxy := datasource.NewProxy()
	proxy.RegisterDatasource(datasource.PrometheusDatasource, &MockDatasource1ForProcessTasks{})

	taskIDList := make([]processortypes.TaskID, 0)
	for i := 0; i < DefaultConcurrentTaskNum+1; i++ {
		taskID := processortypes.TaskID(fmt.Sprintf("task-%d", i))
		taskIDList = append(taskIDList, taskID)
		mockTask, _ := task.NewTask(datasourcetypes.Metric{Resource: v1.ResourceCPU}, "")
		aggregateTasks.Store(taskID, mockTask)
	}
	mockP := Processor{
		TaskQueue:       q,
		AggregateTasks:  aggregateTasks,
		DatasourceProxy: proxy,
	}
	type testFunc func()
	tests := []struct {
		name string
		testFunc
	}{
		{
			name: "case1",
			testFunc: func() {
				for _, id := range taskIDList {
					q.Add(id)
				}
				time.Sleep(5 * time.Second)
				q.ShutDown()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wg := &sync.WaitGroup{}
			wg.Add(2)
			go func() {
				defer wg.Done()
				mockP.ProcessTasks(context.Background())
			}()
			go func() {
				defer wg.Done()
				tt.testFunc()
			}()
			wg.Wait()
		})
	}
}
