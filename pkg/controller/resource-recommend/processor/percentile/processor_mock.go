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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/percentile/task"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

var mockNamespacedName1 = types.NamespacedName{
	Namespace: "testNamespace1",
	Name:      "testName1",
}

var mockNamespacedName2 = types.NamespacedName{
	Namespace: "testNamespace2",
	Name:      "testName2",
}

var notExistNamespacedName = types.NamespacedName{
	Namespace: "notExistNamespace",
	Name:      "notExistName",
}

var mockMetric1 = datasourcetypes.Metric{
	Namespace:     "testNamespace1",
	Kind:          "deployment",
	APIVersion:    "v1",
	WorkloadName:  "testWorkload1",
	ContainerName: "testContainer1",
	Resource:      "cpu",
}

var emptyTaskMetric = datasourcetypes.Metric{
	Namespace:     "testNamespace1",
	Kind:          "deployment",
	APIVersion:    "v1",
	WorkloadName:  "testWorkload1",
	ContainerName: "testContainer1",
	Resource:      "memory",
}

var notExistTaskIDMetric = datasourcetypes.Metric{
	Namespace:     "testNamespace1",
	Kind:          "deployment",
	APIVersion:    "v1",
	WorkloadName:  "testWorkload2",
	ContainerName: "testContainer123",
	Resource:      "cpu",
}

var valueTypeIllegalMetric = datasourcetypes.Metric{
	Namespace:     "testNamespace1",
	Kind:          "deployment",
	APIVersion:    "v1",
	WorkloadName:  "testWorkload2",
	ContainerName: "testContainer123",
	Resource:      "memory",
}

var notExistMetric = datasourcetypes.Metric{
	Namespace:     "testNamespace1",
	Kind:          "deployment",
	APIVersion:    "v1",
	WorkloadName:  "testWorkload2",
	ContainerName: "testContainer876",
	Resource:      "memory",
}

var (
	mockProcessKey1                    = processortypes.ProcessKey{ResourceRecommendNamespacedName: mockNamespacedName1, Metric: &mockMetric1}
	emptyTaskProcessorKey              = processortypes.ProcessKey{ResourceRecommendNamespacedName: mockNamespacedName1, Metric: &emptyTaskMetric}
	metricIsNilProcessorKey            = processortypes.ProcessKey{ResourceRecommendNamespacedName: mockNamespacedName1, Metric: nil}
	notExistNamespacedNameProcessorKey = processortypes.ProcessKey{ResourceRecommendNamespacedName: notExistNamespacedName, Metric: &notExistTaskIDMetric}
	notExistMetricProcessorKey         = processortypes.ProcessKey{ResourceRecommendNamespacedName: mockNamespacedName1, Metric: &notExistMetric}
	notFoundTaskProcessorKey           = processortypes.ProcessKey{ResourceRecommendNamespacedName: mockNamespacedName2, Metric: &notExistTaskIDMetric}
	valueTypeIllegalProcessorKey       = processortypes.ProcessKey{ResourceRecommendNamespacedName: mockNamespacedName2, Metric: &valueTypeIllegalMetric}
)

var (
	mockProcessConfig1            = processortypes.ProcessConfig{ProcessKey: mockProcessKey1}
	emptyProcessConfig            = processortypes.ProcessConfig{ProcessKey: emptyTaskProcessorKey}
	valueTypeIllegalProcessConfig = processortypes.ProcessConfig{ProcessKey: valueTypeIllegalProcessorKey}
)

var (
	mockTaskID1            = mockProcessConfig1.GenerateTaskID()
	emptyTaskID            = emptyProcessConfig.GenerateTaskID()
	notExistTaskID         = "NotExistTaskID"
	valueTypeIllegalTaskID = valueTypeIllegalProcessConfig.GenerateTaskID()
)

var (
	mockTask1, _               = task.NewTask(mockMetric1, "")
	mockTask195PercentileValue = 20.40693
	emptyTask, _               = task.NewTask(emptyTaskMetric, "")
)

var mockAggregateTasks = sync.Map{}

var mockResourceRecommendTaskIDsMap = map[types.NamespacedName]*map[datasourcetypes.Metric]processortypes.TaskID{}

var mockProcessor = Processor{
	AggregateTasks:              &mockAggregateTasks,
	TaskQueue:                   workqueue.NewNamedRateLimitingQueue(DefaultQueueRateLimiter, ProcessorName),
	ResourceRecommendTaskIDsMap: mockResourceRecommendTaskIDsMap,
}

func init() {
	mockAggregateTasks.LoadOrStore(mockTaskID1, mockTask1)
	mockAggregateTasks.LoadOrStore(emptyTaskID, emptyTask)
	mockAggregateTasks.LoadOrStore(valueTypeIllegalTaskID, 3)

	mockResourceRecommendTaskIDsMap[mockNamespacedName1] = &map[datasourcetypes.Metric]processortypes.TaskID{
		mockMetric1:     mockTaskID1,
		emptyTaskMetric: emptyTaskID,
	}
	mockResourceRecommendTaskIDsMap[mockNamespacedName2] = &map[datasourcetypes.Metric]processortypes.TaskID{
		notExistTaskIDMetric:   processortypes.TaskID(notExistTaskID),
		valueTypeIllegalMetric: valueTypeIllegalTaskID,
	}

	sampleTime1 := time.Now()
	mockTask1.AddSample(sampleTime1.Add(-time.Hour*24), 20, 1000)
	mockTask1.AddSample(sampleTime1, 10, 1)
}
