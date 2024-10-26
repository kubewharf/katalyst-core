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
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/percentile/task"
	"github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/log"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
)

func (p *Processor) GarbageCollector(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			errMsg := "garbage collect goroutine panic"
			log.ErrorS(ctx, r.(error), errMsg, "stack", string(debug.Stack()))
			panic(errMsg)
		}
	}()

	ticker := time.Tick(DefaultGarbageCollectInterval)
	for range ticker {
		if err := p.garbageCollect(NewContext()); err != nil {
			log.ErrorS(ctx, err, "garbageCollect failed")
		}
	}
}

func (p *Processor) garbageCollect(ctx context.Context) error {
	defer func() {
		if r := recover(); r != nil {
			errMsg := "garbage collect run panic"
			log.ErrorS(ctx, r.(error), errMsg, "stack", string(debug.Stack()))
		}
	}()

	log.InfoS(ctx, "garbage collect start")

	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Tasks that have not been running for too long are considered invalid and will be cleared
	p.AggregateTasks.Range(func(ki, vi any) bool {
		taskID := ki.(processortypes.TaskID)
		t, ok := vi.(*task.HistogramTask)
		if !ok {
			p.AggregateTasks.Delete(taskID)
			return true
		}
		if t.IsTimeoutNotExecute() {
			p.AggregateTasks.Delete(taskID)
		}
		return true
	})

	// List all ResourceRecommend CR up to now
	resourceRecommendItems, err := p.Lister.List(labels.Everything())
	if err != nil {
		log.ErrorS(ctx, err, "garbage collect list all ResourceRecommend failed")
		return err
	}

	// p.ClearingNoAttributionTask(resourceRecommendList)
	klog.InfoS("percentile processor garbage collect list ResourceRecommend",
		"ResourceRecommend Count", len(resourceRecommendItems))
	// Convert the ResourceRecommend list to map for quick check whether it exists
	existResourceRecommends := make(map[types.NamespacedName]v1alpha1.CrossVersionObjectReference)
	for _, existResourceRecommend := range resourceRecommendItems {
		namespacedName := types.NamespacedName{
			Name:      existResourceRecommend.Name,
			Namespace: existResourceRecommend.Namespace,
		}
		existResourceRecommends[namespacedName] = existResourceRecommend.Spec.TargetRef
	}

	// Delete tasks belong to not exist ResourceRecommend or task not match the targetRef in the ResourceRecommend
	for namespacedName, tasks := range p.ResourceRecommendTaskIDsMap {
		if tasks == nil {
			delete(p.ResourceRecommendTaskIDsMap, namespacedName)
			continue
		}
		targetRef, isExist := existResourceRecommends[namespacedName]
		for metrics, taskID := range *tasks {
			if !isExist ||
				targetRef.Kind != metrics.Kind ||
				targetRef.Name != metrics.WorkloadName ||
				targetRef.APIVersion != metrics.APIVersion {
				p.AggregateTasks.Delete(taskID)
				delete(*tasks, metrics)
			}
			//  timeout task in p.AggregateTasks will clean,
			// If taskID does not exist in p.AggregateTasks，mean is the task is cleared, this needs to be deleted from tasks
			if _, found := p.AggregateTasks.Load(taskID); !found {
				delete(*tasks, metrics)
			}
		}
		if !isExist {
			delete(p.ResourceRecommendTaskIDsMap, namespacedName)
		}
	}

	log.InfoS(ctx, "garbage collect end")
	return nil
}
