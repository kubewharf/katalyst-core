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
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/percentile/task"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

func TestProcessor_garbageCollect(t *testing.T) {
	controlCtx, err := katalystbase.GenerateFakeGenericContext()
	if err != nil {
		t.Fatal(err)
	}
	processor := Processor{
		Lister:                      controlCtx.InternalInformerFactory.Recommendation().V1alpha1().ResourceRecommends().Lister(),
		AggregateTasks:              &sync.Map{},
		ResourceRecommendTaskIDsMap: make(map[types.NamespacedName]*map[datasourcetypes.Metric]processortypes.TaskID),
	}

	namespacedName0 := types.NamespacedName{
		Name:      "testGC0",
		Namespace: "testGC",
	}

	// case1: clean TypeIllegal task
	taskIDTaskTypeIllegal := processortypes.TaskID("taskID_TaskTypeIllegal")
	metricTaskTypeIllegal := datasourcetypes.Metric{
		Namespace:     "testGCTaskTypeIllegal",
		Kind:          "deployment",
		APIVersion:    "v1",
		WorkloadName:  "testGCTaskTypeIllegal",
		ContainerName: "testGCTaskTypeIllegal-1",
		Resource:      v1.ResourceCPU,
	}

	// case2: clean timeout task
	taskID0 := processortypes.TaskID("taskID0")
	metric0 := datasourcetypes.Metric{
		Namespace:     "testGC",
		Kind:          "deployment",
		APIVersion:    "v1",
		WorkloadName:  "testGC0",
		ContainerName: "testGC0-1",
		Resource:      v1.ResourceCPU,
	}
	task0, _ := task.NewTask(metric0, "")
	task0.AddSample(time.Unix(1693572914, 0), 20, 1000)

	// case3: clean resourceRecommend cr not exist task
	taskID1 := processortypes.TaskID("taskID1")
	metric1 := datasourcetypes.Metric{
		Namespace:     "testGC",
		Kind:          "deployment",
		APIVersion:    "v1",
		WorkloadName:  "testGC1",
		ContainerName: "testGC1-1",
		Resource:      v1.ResourceCPU,
	}
	task1, _ := task.NewTask(metric1, "")

	processor.AggregateTasks.LoadOrStore(taskIDTaskTypeIllegal, 3)
	processor.AggregateTasks.LoadOrStore(taskID0, task0)
	processor.AggregateTasks.LoadOrStore(taskID1, task1)
	processor.ResourceRecommendTaskIDsMap[namespacedName0] = &map[datasourcetypes.Metric]processortypes.TaskID{
		metric0:               taskID0,
		metric1:               taskID1,
		metricTaskTypeIllegal: taskIDTaskTypeIllegal,
	}

	tests := []struct {
		name    string
		wantErr bool
	}{
		{},
	}
	for _, tt := range tests {
		err := processor.garbageCollect(NewContext())
		if (err != nil) != tt.wantErr {
			t.Errorf("garbageCollect() error = %v, wantErr %v", err, tt.wantErr)
			return
		}
		if _, ok := processor.AggregateTasks.Load(taskIDTaskTypeIllegal); ok {
			t.Errorf("garbageCollect() type tllegal task not cleaned up")
		}
		if _, ok := processor.AggregateTasks.Load(taskID0); ok {
			t.Errorf("garbageCollect() timeout task not cleaned up")
		}
		if _, ok := processor.AggregateTasks.Load(taskID1); ok {
			t.Errorf("garbageCollect() resourceRecommend cr not exist tasks(in AggregateTasks) not cleaned up")
		}
		if _, ok := processor.ResourceRecommendTaskIDsMap[namespacedName0]; ok {
			t.Errorf("garbageCollect() resourceRecommend cr not exist tasks(in ResourceRecommendTaskIDsMap) not cleaned up")
		}
	}
}
