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
	"runtime/debug"
	"sync"
	"time"

	"github.com/pkg/errors"
	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/client/listers/recommendation/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/datasource"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/percentile/task"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/log"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

const (
	ProcessorName = "percentile"
	// DefaultConcurrentTaskNum is num of default concurrent task
	DefaultConcurrentTaskNum      = 100
	DefaultPercentile             = 0.9
	DefaultGarbageCollectInterval = 1 * time.Hour
	ExceptionRequeueBaseDelay     = time.Minute
	ExceptionRequeueMaxDelay      = 30 * time.Minute
)

type Processor struct {
	mutex sync.Mutex

	Lister v1alpha1.ResourceRecommendLister

	DatasourceProxy *datasource.Proxy

	TaskQueue workqueue.RateLimitingInterface

	AggregateTasks *sync.Map

	// Stores taskID corresponding to Metrics in the ResourceRecommend
	ResourceRecommendTaskIDsMap map[types.NamespacedName]*map[datasourcetypes.Metric]processortypes.TaskID
}

var DefaultQueueRateLimiter = workqueue.NewMaxOfRateLimiter(
	workqueue.NewItemExponentialFailureRateLimiter(ExceptionRequeueBaseDelay, ExceptionRequeueMaxDelay),
	// 10 qps, 100 bucket size.  This is only for retry speed and its only the overall factor (not per item)
	&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
)

func NewProcessor(datasourceProxy *datasource.Proxy, lister v1alpha1.ResourceRecommendLister) processor.Processor {
	return &Processor{
		DatasourceProxy:             datasourceProxy,
		TaskQueue:                   workqueue.NewNamedRateLimitingQueue(DefaultQueueRateLimiter, ProcessorName),
		Lister:                      lister,
		AggregateTasks:              &sync.Map{},
		ResourceRecommendTaskIDsMap: make(map[types.NamespacedName]*map[datasourcetypes.Metric]processortypes.TaskID),
	}
}

func (p *Processor) Register(processConfig *processortypes.ProcessConfig) (cErr *errortypes.CustomError) {
	defer func() {
		if cErr != nil {
			klog.ErrorS(cErr, "Percentile task register failed", "ResourceRecommend", processConfig.ResourceRecommendNamespacedName)
		}
		if r := recover(); r != nil {
			errMsg := "percentile process register panic"
			klog.ErrorS(r.(error), errMsg, "stack", string(debug.Stack()))
			cErr = errortypes.RegisterProcessTaskPanic()
		}
	}()

	if err := processConfig.Validate(); err != nil {
		return errortypes.RegisterProcessTaskValidateError(err)
	}

	taskID := processConfig.GenerateTaskID()

	// Check whether a task has been registered and avoid repeated registration
	_, ok := p.AggregateTasks.Load(taskID)
	if ok {
		klog.V(4).InfoS("The Percentile Processor task already registered", "processConfig", general.StructToString(processConfig))
		return nil
	}

	p.mutex.Lock()
	defer p.mutex.Unlock()

	klog.InfoS("Register Percentile Processor Task", "processConfig", general.StructToString(processConfig))

	metric := *processConfig.Metric

	t, err := task.NewTask(metric, processConfig.Config)
	if err != nil {
		cErr := errortypes.NewProcessTaskError(err)
		return cErr
	}

	_, loaded := p.AggregateTasks.LoadOrStore(taskID, t)
	if !loaded {
		p.TaskQueue.Add(taskID)
	}

	// Record the taskID corresponding to the Metric with the same ResourceRecommendID into ResourceRecommendTaskIDsMap
	// To get the taskID from the ResourceRecommendID and Metric
	if tasks, ok := p.ResourceRecommendTaskIDsMap[processConfig.ResourceRecommendNamespacedName]; !ok {
		p.ResourceRecommendTaskIDsMap[processConfig.ResourceRecommendNamespacedName] = &map[datasourcetypes.Metric]processortypes.TaskID{
			metric: taskID,
		}
	} else {
		if existingTaskID, exist := (*tasks)[metric]; exist {
			if existingTaskID == taskID {
				return nil
			}
			// existingTaskID != taskID means that the config of the task has changed.
			// Need to delete the old task of config
			p.AggregateTasks.Delete(existingTaskID)
		}
		(*tasks)[metric] = taskID
	}

	return nil
}

func (p *Processor) Cancel(processKey *processortypes.ProcessKey) (cErr *errortypes.CustomError) {
	if processKey == nil {
		return nil
	}

	defer func() {
		if cErr != nil {
			klog.ErrorS(cErr, "Percentile task cancel failed", "ResourceRecommend", processKey.ResourceRecommendNamespacedName)
		}
		if r := recover(); r != nil {
			errMsg := "percentile process cancel panic"
			klog.ErrorS(r.(error), errMsg, "stack", string(debug.Stack()))
			cErr = errortypes.CancelProcessTaskPanic()
		}
	}()

	p.mutex.Lock()
	defer p.mutex.Unlock()

	tasks, ok := p.ResourceRecommendTaskIDsMap[processKey.ResourceRecommendNamespacedName]
	if !ok {
		klog.InfoS("Cancel task failed, percentile process task not found", "ResourceRecommend",
			processKey.ResourceRecommendNamespacedName)
		return errortypes.NotFoundTasksError(processKey.ResourceRecommendNamespacedName)
	}
	if processKey.Metric == nil {
		klog.InfoS("delete percentile process tasks", "processConfig", processKey)
		for _, taskID := range *tasks {
			p.AggregateTasks.Delete(taskID)
		}
		delete(p.ResourceRecommendTaskIDsMap, processKey.ResourceRecommendNamespacedName)
	} else {
		if taskID, exist := (*tasks)[*processKey.Metric]; exist {
			klog.InfoS("percentile process task delete for", "processConfig", processKey)
			p.AggregateTasks.Delete(taskID)
			delete(*tasks, *processKey.Metric)
		} else {
			klog.InfoS("task for metric cannot be found, don't deleted", "processConfig", processKey, "metric", processKey.Metric)
		}
		if tasks == nil || len(*tasks) == 0 {
			delete(p.ResourceRecommendTaskIDsMap, processKey.ResourceRecommendNamespacedName)
		}
	}

	return nil
}

func (p *Processor) Run(ctx context.Context) {
	log.InfoS(ctx, "percentile processor starting")

	// Get task from queue and run it
	go p.ProcessTasks(ctx)

	// Garbage collect every hour. Clearing timeout or no attribution task
	go p.GarbageCollector(ctx)

	log.InfoS(ctx, "percentile processor running")

	<-ctx.Done()

	log.InfoS(ctx, "percentile processor end")
}

func (p *Processor) QueryProcessedValues(processKey *processortypes.ProcessKey) (float64, error) {
	t, err := p.getTaskForProcessKey(processKey)
	if err != nil {
		return 0, errors.Wrapf(err, "internal err, process task not found")
	}
	percentileValue, err := t.QueryPercentileValue(NewContext(), DefaultPercentile)
	if err != nil {
		return 0, err
	}
	return percentileValue, nil
}
