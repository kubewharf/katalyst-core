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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestAsyncWorkers(t *testing.T) {
	t.Parallel()

	rt := require.New(t)

	asw := NewAsyncWorkers("test", metrics.DummyMetrics{})

	result, a, b, c, d, e, f, g, h := 0, 1, 2, 3, 4, 5, 6, 7, 8

	timeoutSeconds := 100 * time.Millisecond
	fn := func(ctx context.Context, params ...interface{}) error {
		if len(params) != 2 {
			return fmt.Errorf("invalid params")
		}

		time.Sleep(5 * time.Millisecond)
		p1Int := params[0].(int)
		p2Int := params[1].(int)
		result = p1Int + p2Int
		_ = EmitAsyncedMetrics(ctx, metrics.MetricTag{})
		return nil
	}

	work1Name := "work1"
	work1DeliveredAt := time.Now()
	work1 := &Work{
		Fn:          fn,
		Params:      []interface{}{a, b},
		DeliveredAt: work1DeliveredAt,
	}

	err := asw.AddWork(work1Name, work1, DuplicateWorkPolicyOverride)
	rt.Nil(err)
	asw.workLock.Lock()
	rt.NotNil(asw.workStatuses[work1Name])
	asw.workLock.Unlock()

	asw.workLock.Lock()
	for asw.workStatuses[work1Name].working {
		asw.workLock.Unlock()
		time.Sleep(10 * time.Millisecond)

		if time.Now().Sub(work1DeliveredAt) > timeoutSeconds {
			rt.Failf("%s timeout", work1Name)
		}

		asw.workLock.Lock()
	}
	asw.workLock.Unlock()

	rt.Equal(result, a+b)

	work2Name := "work2"
	work2DeliveredAt := time.Now()
	work2 := &Work{
		Fn:          fn,
		Params:      []interface{}{c, d},
		DeliveredAt: work2DeliveredAt,
	}

	err = asw.AddWork(work2Name, work2, DuplicateWorkPolicyOverride)
	rt.Nil(err)
	asw.workLock.Lock()
	rt.NotNil(asw.workStatuses[work2Name])
	rt.Nil(asw.lastUndeliveredWork[work2Name])
	asw.workLock.Unlock()

	work3DeliveredAt := time.Now()
	work3 := &Work{
		Fn:          fn,
		Params:      []interface{}{e, f},
		DeliveredAt: work3DeliveredAt,
	}

	err = asw.AddWork(work2Name, work3, DuplicateWorkPolicyOverride)
	rt.Nil(err)
	asw.workLock.Lock()
	rt.NotNil(asw.lastUndeliveredWork[work2Name])
	asw.workLock.Unlock()

	work4DeliveredAt := time.Now()
	work4 := &Work{
		Fn:          fn,
		Params:      []interface{}{g, h},
		DeliveredAt: work4DeliveredAt,
	}

	err = asw.AddWork(work2Name, work4, DuplicateWorkPolicyDiscard)
	rt.Nil(err)
	asw.workLock.Lock()
	rt.Equal(work3, asw.lastUndeliveredWork[work2Name])
	asw.workLock.Unlock()

	asw.workLock.Lock()
	for asw.workStatuses[work2Name].working {
		asw.workLock.Unlock()
		time.Sleep(10 * time.Millisecond)

		if time.Now().Sub(work1DeliveredAt) > 3*timeoutSeconds {
			rt.Failf("%s timeout", work2Name)
		}

		asw.workLock.Lock()
	}
	asw.workLock.Unlock()

	rt.Equal(result, e+f)
}

var (
	res = map[string]string{}
	mu  sync.Mutex
)

func newWork(name string, a string) *Work {
	fn := func(ctx context.Context, params ...interface{}) error {
		time.Sleep(time.Millisecond)
		p0 := params[0].(string)
		mu.Lock()
		defer mu.Unlock()
		res[name] = p0
		return nil
	}

	work := &Work{
		Name:        name,
		Fn:          fn,
		Params:      []interface{}{a},
		DeliveredAt: time.Now(),
	}
	return work
}

func TestAsyncLimitedWorkers(t *testing.T) {
	t.Parallel()

	type addWorkFunc func(alw *AsyncLimitedWorkers)

	tests := []struct {
		name             string
		addWorkFunc      addWorkFunc
		wantActiveQKeys  []string
		wantBackoffQKeys []string
		wantWaitQKeys    []string
		wantRet          map[string]string
	}{
		{
			name: "same work to discard",
			addWorkFunc: func(alw *AsyncLimitedWorkers) {
				w := newWork("w1", "1")
				alw.AddWork(w, DuplicateWorkPolicyDiscard)

				_, _, _ = alw.poll(wait.NeverStop)

				w = newWork("w1", "2")
				alw.AddWork(w, DuplicateWorkPolicyDiscard)
			},
			wantActiveQKeys:  []string{"w1"},
			wantWaitQKeys:    []string{},
			wantBackoffQKeys: []string{},
		},
		{
			name: "same work to override",
			addWorkFunc: func(alw *AsyncLimitedWorkers) {
				w := newWork("w1", "1")
				alw.AddWork(w, DuplicateWorkPolicyDiscard)

				_, _, _ = alw.poll(wait.NeverStop)

				w = newWork("w1", "2")
				alw.AddWork(w, DuplicateWorkPolicyOverride)
			},
			wantActiveQKeys:  []string{"w1"},
			wantWaitQKeys:    []string{},
			wantBackoffQKeys: []string{"w1"},
		},
		{
			name: "2 diff work to add",
			addWorkFunc: func(alw *AsyncLimitedWorkers) {
				w := newWork("w1", "1")
				alw.AddWork(w, DuplicateWorkPolicyDiscard)
				_, _, _ = alw.poll(wait.NeverStop)

				w = newWork("w2", "1")
				alw.AddWork(w, DuplicateWorkPolicyOverride)
				_, _, _ = alw.poll(wait.NeverStop)
			},
			wantActiveQKeys:  []string{"w1", "w2"},
			wantWaitQKeys:    []string{},
			wantBackoffQKeys: []string{},
		},
		{
			name: "3 diff work to add",
			addWorkFunc: func(alw *AsyncLimitedWorkers) {
				w := newWork("w1", "1")
				alw.AddWork(w, DuplicateWorkPolicyDiscard)
				_, _, _ = alw.poll(wait.NeverStop)

				w = newWork("w2", "1")
				alw.AddWork(w, DuplicateWorkPolicyOverride)
				_, _, _ = alw.poll(wait.NeverStop)

				w = newWork("w3", "1")
				alw.AddWork(w, DuplicateWorkPolicyOverride)
			},
			wantActiveQKeys:  []string{"w1", "w2"},
			wantWaitQKeys:    []string{"w3"},
			wantBackoffQKeys: []string{},
		},
		{
			name: "3 diff work to handle",
			addWorkFunc: func(alw *AsyncLimitedWorkers) {
				w := newWork("w1", "1")
				alw.AddWork(w, DuplicateWorkPolicyDiscard)
				ctx, work, _ := alw.poll(wait.NeverStop)
				alw.doHandle(ctx, work)

				w = newWork("w2", "1")
				alw.AddWork(w, DuplicateWorkPolicyOverride)
				ctx, work, _ = alw.poll(wait.NeverStop)
				alw.doHandle(ctx, work)

				w = newWork("w3", "1")
				alw.AddWork(w, DuplicateWorkPolicyOverride)
			},
			wantActiveQKeys:  []string{},
			wantWaitQKeys:    []string{"w3"},
			wantBackoffQKeys: []string{},
			wantRet:          map[string]string{"w1": "1", "w2": "2"},
		},
		{
			name: "4 diff work to add",
			addWorkFunc: func(alw *AsyncLimitedWorkers) {
				w := newWork("w1", "1")
				alw.AddWork(w, DuplicateWorkPolicyDiscard)

				w = newWork("w2", "1")
				alw.AddWork(w, DuplicateWorkPolicyOverride)

				w = newWork("w3", "1")
				alw.AddWork(w, DuplicateWorkPolicyOverride)

				w = newWork("w4", "1")
				alw.AddWork(w, DuplicateWorkPolicyOverride)

				ctx, work, _ := alw.poll(wait.NeverStop)
				alw.doHandle(ctx, work)

				ctx, work, _ = alw.poll(wait.NeverStop)
				alw.doHandle(ctx, work)

				ctx, work, _ = alw.poll(wait.NeverStop)
				alw.doHandle(ctx, work)
			},
			wantActiveQKeys:  []string{},
			wantWaitQKeys:    []string{"w4"},
			wantBackoffQKeys: []string{},
			wantRet:          map[string]string{"w1": "1", "w2": "1", "w3": "1"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			alw := NewAsyncLimitedWorkers("test", 2, metrics.DummyMetrics{})
			tt.addWorkFunc(alw)

			assert.ElementsMatch(t, tt.wantWaitQKeys, alw.waitQ.ListKeys())

			var activeQKeys []string
			for key := range alw.activeQ {
				activeQKeys = append(activeQKeys, key)
			}
			assert.ElementsMatch(t, tt.wantActiveQKeys, activeQKeys)

			var backoffQKeys []string
			for key := range alw.backoffQ {
				backoffQKeys = append(backoffQKeys, key)
			}
			assert.ElementsMatch(t, tt.wantBackoffQKeys, backoffQKeys)
		})
	}
}
