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

package server

import (
	"context"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type fanoutNotifier struct {
	sync.RWMutex
	receptacles map[context.Context]chan struct{}
}

func newNotifier() *fanoutNotifier {
	return &fanoutNotifier{
		receptacles: make(map[context.Context]chan struct{}),
	}
}

func (n *fanoutNotifier) Notify() {
	n.RLock()
	defer n.RUnlock()

	for c, v := range n.receptacles {
		clientErr := c.Err()
		if clientErr != nil {
			general.Warningf("pap: power capping server: client communication failed: %v", clientErr)
			break
		}

		if len(v) >= 1 {
			general.Warningf("pap: power capping server: client not fetching req timely")
			break
		}
		v <- struct{}{}
	}
}

func (n *fanoutNotifier) IsEmpty() bool {
	n.RLock()
	defer n.RUnlock()

	return len(n.receptacles) == 0
}

func (n *fanoutNotifier) Register(ctx context.Context) <-chan struct{} {
	n.Lock()
	defer n.Unlock()

	if ch, ok := n.receptacles[ctx]; ok {
		general.Warningf("pap: power capping server: signal slot already registered")
		return ch
	}

	// chan of size 1 to decouple producer and consumer
	ctxChan := make(chan struct{}, 1)
	n.receptacles[ctx] = ctxChan
	return ctxChan
}

func (n *fanoutNotifier) Unregister(ctx context.Context) {
	n.Lock()
	defer n.Unlock()

	delete(n.receptacles, ctx)
}
