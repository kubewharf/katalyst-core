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

package asyncworker

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func workKeyFunc(obj interface{}) (string, error) {
	w, ok := obj.(*Work)
	if !ok {
		return "", fmt.Errorf("invalid work type")
	}
	return w.Name, nil
}

func workLessFunc(w1, w2 interface{}) bool {
	return w1.(*Work).DeliveredAt.Before(w2.(*Work).DeliveredAt)
}

func NewAsyncLimitedWorkers(name string, limit int, emitter metrics.MetricEmitter) *AsyncLimitedWorkers {
	alw := &AsyncLimitedWorkers{
		name:    name,
		limit:   limit,
		emitter: emitter,

		activeQ:  map[string]*workStatus{},
		waitQ:    cache.NewHeap(workKeyFunc, workLessFunc),
		backoffQ: map[string]*Work{},
	}
	alw.cond.L = &alw.workLock
	return alw
}

func (alw *AsyncLimitedWorkers) AddWork(work *Work, policy DuplicateWorkPolicy) error {
	alw.workLock.Lock()
	defer alw.workLock.Unlock()

	err := validateWork(work)
	if err != nil {
		return fmt.Errorf("validateWork for: %s failed with error: %v", work.Name, err)
	}

	general.InfoS("add work",
		"AsyncLimitedWorkers", alw.name,
		"workName", work.Name,
		"UID", work.UID,
		"params", work.Params,
		"deliveredAt", work.DeliveredAt)

	status, ok := alw.activeQ[work.Name]
	if ok {
		if policy == DuplicateWorkPolicyDiscard {
			general.InfoS("work already exists, discard new work", "name", work.Name, "uid", work.UID)
			return nil
		}

		var oldUID types.UID
		if old, ok := alw.backoffQ[work.Name]; ok {
			oldUID = old.UID
		}

		alw.backoffQ[work.Name] = work
		status.cancelFn()
		general.InfoS("work is in activeQ, push it to backoffQ", "name", work.Name, "uid", work.UID, "oldUID", oldUID)
	} else {
		var oldUID types.UID
		obj, existed, err := alw.waitQ.GetByKey(work.Name)
		if err != nil {
			return err
		}
		if existed {
			oldUID = obj.(*Work).UID
		}

		alw.waitQ.Add(work)
		general.InfoS("work is not in activeQ, push it to waitQ", "name", work.Name, "uid", work.UID, "oldUID", oldUID)
	}

	alw.cond.Broadcast()

	return nil
}

func (alw *AsyncLimitedWorkers) poll(stopCh <-chan struct{}) (context.Context, *Work, error) {
	alw.workLock.Lock()
	work, err := alw.doPoll(stopCh)
	if err != nil {
		return nil, nil, err
	}
	ctx := alw.prepareWork(work)
	alw.workLock.Unlock()
	return ctx, work, nil
}

func (alw *AsyncLimitedWorkers) doPoll(stopCh <-chan struct{}) (*Work, error) {
	for alw.limit <= len(alw.activeQ) || len(alw.waitQ.ListKeys()) <= 0 {
		alw.cond.Wait()
		select {
		case <-stopCh:
			return nil, fmt.Errorf("stopCh closed")
		default:
		}
	}

	general.InfoS("work queues length", "activeQ", len(alw.activeQ), "waitQ", len(alw.waitQ.ListKeys()), "backoffQ", len(alw.backoffQ))

	item, err := alw.waitQ.Pop()
	if err != nil {
		return nil, err
	}
	work, ok := item.(*Work)
	if !ok {
		return nil, fmt.Errorf("invalid work type")
	}
	return work, nil
}

func (alw *AsyncLimitedWorkers) prepareWork(work *Work) context.Context {
	if work == nil {
		general.Fatalf("[AsyncWorkers: %s] contextForWork: %s got nil work", alw.name, work.Name)
	}

	ctx := context.Background()
	if names := strings.Split(work.Name, WorkNameSeperator); len(names) > 0 {
		ctx = context.WithValue(ctx, contextKeyMetricName, names[len(names)-1])
		ctx = context.WithValue(ctx, contextKeyMetricEmitter, alw.emitter)
	}

	status := &workStatus{
		working:   true,
		startedAt: time.Now(),
		work:      work,
	}
	status.ctx, status.cancelFn = context.WithCancel(ctx)
	alw.activeQ[work.Name] = status
	return status.ctx
}

func (alw *AsyncLimitedWorkers) doHandle(ctx context.Context, work *Work) {
	var handleErr error

	defer func() {
		if r := recover(); r != nil {
			handleErr = fmt.Errorf("recover from %v", r)

			metricErr := EmitCustomizedAsyncedMetrics(ctx,
				metricNameAsyncWorkPanic, 1,
				metrics.ConvertMapToTags(map[string]string{
					"workName": work.Name,
				})...)

			if metricErr != nil {
				general.Errorf("emit metric(%s:%d) failed with err: %v",
					metricNameAsyncWorkDurationMs, 1, metricErr)
			}
		}

		alw.complete(work, handleErr)
	}()

	general.InfoS("handle work",
		"AsyncLimitedWorkers", alw.name,
		"workName", work.Name,
		"uid", work.UID,
		"params", work.Params,
		"deliveredAt", work.DeliveredAt)

	funcValue := reflect.ValueOf(work.Fn)

	// filling up parameters for the passed functions
	paramValues := make([]reflect.Value, 1, len(work.Params)+1)
	paramValues[0] = reflect.ValueOf(ctx)
	for _, param := range work.Params {
		paramValues = append(paramValues, reflect.ValueOf(param))
	}

	startTime := time.Now()
	funcRets := funcValue.Call(paramValues)
	workDurationMs := time.Since(startTime).Milliseconds()
	waitDurationMs := startTime.Sub(work.DeliveredAt).Milliseconds()

	if len(funcRets) != 1 {
		handleErr = fmt.Errorf("work Fn returns invalid number: %d of return values", len(funcRets))
	} else if funcRets[0].Interface() != nil {
		var ok bool
		handleErr, ok = funcRets[0].Interface().(error)

		if !ok {
			handleErr = fmt.Errorf("work Fn returns return value: %v of invalid type", funcRets[0].Interface())
		}
	}

	if metricErr := EmitCustomizedAsyncedMetrics(ctx,
		metricNameAsyncWorkDurationMs, workDurationMs,
		metrics.ConvertMapToTags(map[string]string{
			"workName": work.Name,
		})...); metricErr != nil {
		general.Errorf("emit metric(%s:%d) failed with err: %v",
			metricNameAsyncWorkDurationMs, workDurationMs, metricErr)
	}

	if metricErr := EmitCustomizedAsyncedMetrics(ctx,
		metricNameAsyncWorkWaitingMs, waitDurationMs,
		metrics.ConvertMapToTags(map[string]string{
			"workName": work.Name,
		})...); metricErr != nil {
		general.Errorf("emit metric(%s:%d) failed with err: %v",
			metricNameAsyncWorkWaitingMs, waitDurationMs, metricErr)
	}
}

func (alw *AsyncLimitedWorkers) complete(completedWork *Work, workErr error) {
	alw.workLock.Lock()
	defer alw.workLock.Unlock()

	status, ok := alw.activeQ[completedWork.Name]
	if !ok {
		general.Warningf("work %v not in activeQ", completedWork.Name)
	} else {
		status.finishedAt = time.Now()
		general.InfoS("complete work",
			"AsyncLimitedWorkers", alw.name,
			"workName", completedWork.Name,
			"uid", completedWork.UID,
			"params", completedWork.Params,
			"deliveredAt", completedWork.DeliveredAt,
			"startAt", status.startedAt,
			"finishAt", status.finishedAt,
			"workErr", workErr)
	}

	work, ok := alw.backoffQ[completedWork.Name]
	if ok {
		alw.waitQ.Add(work)
		delete(alw.backoffQ, completedWork.Name)
	}
	delete(alw.activeQ, completedWork.Name)

	alw.cond.Broadcast()
}

func (alw *AsyncLimitedWorkers) Start(stopCh <-chan struct{}) error {
	go wait.Until(func() {
		for {
			ctx, work, err := alw.poll(stopCh)
			if err != nil {
				general.ErrorS(err, "failed to poll work")
				return
			}

			go alw.doHandle(ctx, work)
		}
	}, 10*time.Second, stopCh)

	go func() {
		<-stopCh
		alw.cond.Broadcast()
	}()
	return nil
}
