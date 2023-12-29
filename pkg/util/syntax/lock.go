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

/*
 native locking functions in package-sync doesn't contain deadlock-detecting logic,
 to avoid deadlock during runtime processes, it's advised to use locking functions
 here to provide deadlock-detecting when it exceeds timeout
*/

package syntax

import (
	"runtime"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const defaultDeadlockPeriod = time.Minute * 5

const (
	metricsNameLockTimeout  = "lock_timeout"
	metricsNameRLockTimeout = "rlock_timeout"
)

type Mutex struct {
	sync.Mutex

	emitter  metrics.MetricEmitter
	deadlock time.Duration
}

func NewMutex(emitter metrics.MetricEmitter) *Mutex {
	return &Mutex{
		emitter:  emitter,
		deadlock: defaultDeadlockPeriod,
	}
}

func NewMutexWithPeriod(emitter metrics.MetricEmitter, period time.Duration) *Mutex {
	return &Mutex{
		emitter:  emitter,
		deadlock: period,
	}
}

func (m *Mutex) Lock() int {
	return runWithTimeoutDetect(m.deadlock, m.Mutex.Lock, func() {
		_ = m.emitter.StoreInt64(metricsNameLockTimeout, 1, metrics.MetricTypeNameRaw)
		klog.Infof("potential deadlock Mutex.Lock for caller: %v", getCaller())
	})
}

func (m *Mutex) Unlock() { m.Mutex.Unlock() }

type RWMutex struct {
	sync.RWMutex

	emitter  metrics.MetricEmitter
	deadlock time.Duration
}

func NewRWMutex(emitter metrics.MetricEmitter) *RWMutex {
	return &RWMutex{
		emitter:  emitter,
		deadlock: defaultDeadlockPeriod,
	}
}

func NewRWMutexWithPeriod(emitter metrics.MetricEmitter, period time.Duration) *RWMutex {
	return &RWMutex{
		emitter:  emitter,
		deadlock: period,
	}
}

func (rm *RWMutex) Lock() int {
	return runWithTimeoutDetect(rm.deadlock, rm.RWMutex.Lock, func() {
		_ = rm.emitter.StoreInt64(metricsNameLockTimeout, 1, metrics.MetricTypeNameRaw)
		klog.Infof("potential deadlock RWMutex.Lock for caller: %v", getCaller())
	})
}

func (rm *RWMutex) RLock() int {
	return runWithTimeoutDetect(rm.deadlock, rm.RWMutex.RLock, func() {
		_ = rm.emitter.StoreInt64(metricsNameRLockTimeout, 1, metrics.MetricTypeNameRaw)
		klog.Infof("potential deadlock RWMutex.RLock for caller: %v", getCaller())
	})
}

func (rm *RWMutex) Unlock()  { rm.RWMutex.Unlock() }
func (rm *RWMutex) RUnlock() { rm.RWMutex.RUnlock() }

// getCaller returns the caller info for logging
func getCaller() string {
	pc, _, _, ok := runtime.Caller(4)
	if !ok {
		return ""
	}
	return runtime.FuncForPC(pc).Name()
}

// runWithTimeoutDetect runs the given command, and exec the onTimeout function if timeout
func runWithTimeoutDetect(timeout time.Duration, command, onTimeout func()) int {
	c := make(chan interface{})
	cnt := 0

	go func() {
		for {
			select {
			case <-c:
				return
			case <-time.After(timeout):
				onTimeout()
				cnt++
			}
		}
	}()

	command()
	c <- struct{}{}
	return cnt
}
