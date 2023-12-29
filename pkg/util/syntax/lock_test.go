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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func TestLock(t *testing.T) {
	t.Parallel()

	m := NewMutexWithPeriod(metrics.DummyMetrics{}, time.Second)
	assert.Equal(t, m.Lock(), 0)
	go func() {
		assert.Greater(t, m.Lock(), 0)
		m.Unlock()
	}()
	time.Sleep(time.Second * 2)
	m.Unlock()
	time.Sleep(time.Second)
}

func TestLockMultipleTimes(t *testing.T) {
	t.Parallel()

	m := NewMutexWithPeriod(metrics.DummyMetrics{}, time.Second)
	assert.Equal(t, m.Lock(), 0)
	go func() {
		assert.Greater(t, m.Lock(), 1)
		m.Unlock()
	}()
	time.Sleep(time.Second * 4)
	m.Unlock()
	time.Sleep(time.Second)
}

func TestRWLock(t *testing.T) {
	t.Parallel()

	rm := NewRWMutexWithPeriod(metrics.DummyMetrics{}, time.Second)
	assert.Equal(t, rm.Lock(), 0)
	go func() {
		assert.Greater(t, rm.Lock(), 0)
		rm.Unlock()
	}()
	time.Sleep(time.Second * 2)
	rm.Unlock()
	time.Sleep(time.Second)
}

func TestRWRLock(t *testing.T) {
	t.Parallel()

	rm := NewRWMutexWithPeriod(metrics.DummyMetrics{}, time.Second)
	assert.Equal(t, rm.Lock(), 0)
	go func() {
		assert.Greater(t, rm.RLock(), 0)
		rm.RUnlock()
	}()
	time.Sleep(time.Second * 2)
	rm.Unlock()
	time.Sleep(time.Second)
}
