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

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/percentile/task"
	"github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/log"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

func NewContext() context.Context {
	return log.SetKeysAndValues(log.InitContext(context.Background()), "processor", ProcessorName)
}

func (p *Processor) getTaskForProcessKey(processKey *processortypes.ProcessKey) (*task.HistogramTask, error) {
	if processKey == nil {
		return nil, errors.Errorf("ProcessKey is nil")
	}
	if processKey.Metric == nil {
		return nil, errors.Errorf("Metric is nil")
	}
	if tasks, ok := p.ResourceRecommendTaskIDsMap[processKey.ResourceRecommendNamespacedName]; !ok {
		return nil, errors.Errorf("not found percentile process task ID for ResourceRecommend(%s)",
			processKey.ResourceRecommendNamespacedName)
	} else {
		if taskID, exist := (*tasks)[*processKey.Metric]; exist {
			return p.getTaskForTaskID(taskID)
		}
		return nil, errors.Errorf("not found process task ID for container(%s) in ResourceRecommend(%s)",
			processKey.ContainerName, processKey.ResourceRecommendNamespacedName)
	}
}

func (p *Processor) getTaskForTaskID(taskID processortypes.TaskID) (*task.HistogramTask, error) {
	data, found := p.AggregateTasks.Load(taskID)
	if !found {
		return nil, errors.New("process task not found")
	}
	t, ok := data.(*task.HistogramTask)
	if !ok {
		return nil, errors.New("process task type illegal")
	}
	return t, nil
}
