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

package rule

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

// EvictionQueue aims to build a queue for eviction EvictPods, based on this
// queue, eviction manager can use it to perform more efficient logics, e.g.
// rate limiter, priority-based sorting strategy, eviction withdraw and so on
type EvictionQueue interface {
	// Add EvictPods into queue, whether to perform sorting logic should be added here;
	// and the parameter "override" will decide whether we should withdraw all
	// candidate EvictPods that don't appear in the given pod list
	Add(rpList RuledEvictPodList, override bool)
	// Withdraw withdraws EvictPods from candidate list if exists
	Withdraw(rpList RuledEvictPodList)
	// Pop EvictPods for eviction
	Pop() RuledEvictPodList

	// List returns all EvictPods in this queue (without deleting)
	List() RuledEvictPodList
}

// FIFOEvictionQueue is the default implementation for EvictionQueue;
// it will use the FIFO strategy for one batch of EvictPods (sort for
// those EvictPods in the same way though), and support to pop a limited amount
// of EvictPods when pop is triggered.
type FIFOEvictionQueue struct {
	// limited set slo
	limited int

	sync.Mutex
	rpList RuledEvictPodList
	podIDs map[types.UID]interface{}
}

func NewFIFOEvictionQueue(limited int) EvictionQueue {
	return &FIFOEvictionQueue{
		limited: limited,
		podIDs:  make(map[types.UID]interface{}),
	}
}

func (f *FIFOEvictionQueue) Add(rpList RuledEvictPodList, override bool) {
	f.Lock()
	defer f.Unlock()

	if override {
		f.rpList = RuledEvictPodList{}
		f.podIDs = make(map[types.UID]interface{})
	}

	for _, ep := range rpList {
		if _, ok := f.podIDs[ep.Pod.UID]; !ok {
			f.podIDs[ep.Pod.UID] = struct{}{}
			f.rpList = append(f.rpList, ep)
		}
	}
}

func (f *FIFOEvictionQueue) Withdraw(rpList RuledEvictPodList) {
	if len(rpList) == 0 {
		return
	}

	deleteSets := make(map[types.UID]interface{})
	for _, ep := range rpList {
		deleteSets[ep.Pod.UID] = struct{}{}
	}

	f.Lock()
	defer f.Unlock()

	newRpList := RuledEvictPodList{}
	evictPodIDs := make(map[types.UID]interface{})
	for _, ep := range f.rpList {
		if _, ok := deleteSets[ep.Pod.UID]; !ok {
			newRpList = append(newRpList, ep)
			evictPodIDs[ep.Pod.UID] = struct{}{}
		}
	}

	f.rpList = newRpList
	f.podIDs = evictPodIDs
}

func (f *FIFOEvictionQueue) Pop() RuledEvictPodList {
	f.Lock()
	defer f.Unlock()

	amount := f.limited
	if amount < 0 || amount > len(f.rpList) {
		amount = f.rpList.Len()
	}

	rpList := f.rpList[:amount]
	f.rpList = f.rpList[amount:]
	for _, ep := range rpList {
		delete(f.podIDs, ep.Pod.UID)
	}

	return rpList
}

func (f *FIFOEvictionQueue) List() RuledEvictPodList {
	f.Lock()
	defer f.Unlock()

	return f.rpList
}
