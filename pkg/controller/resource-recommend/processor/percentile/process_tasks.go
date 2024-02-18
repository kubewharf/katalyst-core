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

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/log"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

func (p *Processor) ProcessTasks(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			errMsg := "process tasks goroutine run panic"
			log.ErrorS(ctx, r.(error), errMsg, "stack", string(debug.Stack()))
			panic(errMsg)
		}
	}()

	wg := &sync.WaitGroup{}
	wg.Add(DefaultConcurrentTaskNum)
	for i := 0; i < DefaultConcurrentTaskNum; i++ {
		go func() {
			defer wg.Done()
			// Run a worker thread that just dequeues items, processes them, and marks them done.
			for p.processTask(NewContext()) {
			}
		}()
	}

	log.InfoS(ctx, "process workers running")
	wg.Wait()
	log.InfoS(ctx, "all process workers finished")
}

func (p *Processor) processTask(ctx context.Context) bool {
	obj, shutdown := p.TaskQueue.Get()
	log.InfoS(ctx, "process task dequeue", "obj", obj, "shutdown", shutdown)
	if shutdown {
		err := errors.New("task queue is shutdown")
		log.ErrorS(ctx, err, "process task failed")
		// Stop working
		return false
	}

	defer p.TaskQueue.Done(obj)

	p.taskHandler(ctx, obj)
	return true
}

func (p *Processor) taskHandler(ctx context.Context, obj interface{}) {
	log.InfoS(ctx, "task handler begin")
	taskID, ok := obj.(processortypes.TaskID)
	if !ok {
		err := errors.New("task key type error, drop")
		log.ErrorS(ctx, err, "task err", "obj", obj)
		p.TaskQueue.Forget(obj)
		return
	}
	ctx = log.SetKeysAndValues(ctx, "taskID", taskID)

	task, err := p.getTaskForTaskID(taskID)
	if err != nil {
		log.ErrorS(ctx, err, "task not found, drop it")
		p.TaskQueue.Forget(obj)
		return
	}

	if nextRunInterval, err := task.Run(ctx, p.DatasourceProxy); err != nil {
		log.ErrorS(ctx, err, "task handler err")
		p.TaskQueue.AddRateLimited(taskID)
	} else {
		log.InfoS(ctx, "task handler finished")
		p.TaskQueue.Forget(obj)
		if nextRunInterval > 0 {
			p.TaskQueue.AddAfter(taskID, nextRunInterval)
		}
	}
}
