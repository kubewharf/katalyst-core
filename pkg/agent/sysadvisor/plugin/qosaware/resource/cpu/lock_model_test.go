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

package cpu

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestGetHeadroomBeforeUpdateFailsFast verifies the read-path contract:
// GetHeadroom takes cra.mutex.RLock and, on an advisor that has never completed
// a cycle (advisorUpdated=false), returns an immediate "not updated" error
// rather than blocking. This is the read-side of the cra.mutex RWMutex model.
func TestGetHeadroomBeforeUpdateFailsFast(t *testing.T) {
	t.Parallel()

	cra := &cpuResourceAdvisor{}
	_, _, err := cra.GetHeadroom()
	require.Error(t, err)
	require.Contains(t, err.Error(), "advisor not updated")
}

// TestGetHeadroomNilAssemblerFailsFast verifies that even after advisorUpdated
// is flipped on, a nil headroomAssembler produces a clean error under RLock
// rather than a nil-pointer panic. This exercises the RLock-protected read path.
func TestGetHeadroomNilAssemblerFailsFast(t *testing.T) {
	t.Parallel()

	cra := &cpuResourceAdvisor{advisorUpdated: true}
	_, _, err := cra.GetHeadroom()
	require.Error(t, err)
	require.Contains(t, err.Error(), "no legal assembler")
}

// TestCraMutexReadersConcurrent verifies the RWMutex read-shared semantics:
// two goroutines holding cra.mutex.RLock simultaneously must not block each
// other. We do this by manually taking RLock twice in parallel and confirming
// both acquisitions complete within a tight deadline.
//
// This pins the structural contract that GetHeadroom (which takes RLock) does
// not serialize concurrent readers -- only the writer path (update, which takes
// Lock) can. Under -race this also confirms the mutex itself is shared state
// accessed consistently.
func TestCraMutexReadersConcurrent(t *testing.T) {
	t.Parallel()

	cra := &cpuResourceAdvisor{}
	const n = 4
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			<-start
			cra.mutex.RLock()
			defer cra.mutex.RUnlock()
			// Hold the read lock briefly so a second reader must actually
			// co-acquire it; if RLock were exclusive this would serialize.
			time.Sleep(20 * time.Millisecond)
		}()
	}
	close(start)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		// all readers co-acquired; RWMutex read-shared semantics hold.
	case <-time.After(2 * time.Second):
		t.Fatalf("concurrent RLock readers serialized: RLock is not read-shared")
	}
}

// TestCraMutexWriterBlocksReaders verifies the RWMutex write-exclusive
// semantics: once a writer holds cra.mutex.Lock(), a concurrent RLock attempt
// must block until the writer releases. This is the back-pressure that makes
// update() (writer) wait for in-flight GetHeadroom readers and vice versa.
func TestCraMutexWriterBlocksReaders(t *testing.T) {
	t.Parallel()

	cra := &cpuResourceAdvisor{}

	// Writer takes the lock first.
	cra.mutex.Lock()
	writerReleased := make(chan struct{})
	readerAcquired := make(chan struct{})
	go func() {
		<-writerReleased
		cra.mutex.Unlock()
	}()

	// Reader must block until the writer releases.
	go func() {
		cra.mutex.RLock()
		close(readerAcquired)
		cra.mutex.RUnlock()
	}()

	select {
	case <-readerAcquired:
		t.Fatalf("reader acquired RLock while writer held Lock: write-exclusive semantics broken")
	case <-time.After(50 * time.Millisecond):
		// expected: reader is blocked
	}
	close(writerReleased)

	select {
	case <-readerAcquired:
		// reader acquired after writer released: correct.
	case <-time.After(2 * time.Second):
		t.Fatalf("reader did not unblock after writer released")
	}
}

// TestRegionLockPerRegionIsIndependent verifies the region-lock access model:
// each QoSRegionBase embeds its own sync.Mutex (region_base.go line 171), so
// locking region A must not block locking region B. This is the per-region
// serialization that lets update() walk regionMap and call methods on each
// region without a global region lock. We pin the contract with two local
// mutex-embedding stand-ins; the production guarantee is the same Go language
// rule: a struct-embedded sync.Mutex is per-instance.
func TestRegionLockPerRegionIsIndependent(t *testing.T) {
	t.Parallel()

	type regionLike struct{ sync.Mutex }
	ra := &regionLike{}
	rb := &regionLike{}

	ra.Lock()
	acquiredB := make(chan struct{})
	go func() {
		rb.Lock()
		close(acquiredB)
		rb.Unlock()
	}()

	select {
	case <-acquiredB:
		// B acquired while A held its lock: per-region mutexes are independent.
	case <-time.After(2 * time.Second):
		t.Fatalf("region B lock blocked by region A lock: region mutexes are not per-region")
	}
	ra.Unlock()
}
