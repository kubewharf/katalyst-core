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

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/uuid"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type contextKey struct{}

var fanoutKey = contextKey{}

func wrapFanoutContext(ctx context.Context) context.Context {
	id := uuid.NewUUID()
	return context.WithValue(ctx, fanoutKey, string(id))
}

type receptacle struct {
	ctx context.Context
	ch  chan struct{}
}

type fanoutNotifier struct {
	sync.RWMutex
	receptacles map[string]*receptacle
}

func newNotifier() *fanoutNotifier {
	return &fanoutNotifier{
		receptacles: make(map[string]*receptacle),
	}
}

func (n *fanoutNotifier) Notify() {
	n.RLock()
	defer n.RUnlock()

	for _, recep := range n.receptacles {
		clientErr := recep.ctx.Err()
		if clientErr != nil {
			general.Warningf("pap: power capping server: client communication failed: %v", clientErr)
			continue
		}

		select {
		case recep.ch <- struct{}{}:
			continue
		default:
			general.Warningf("pap: power capping server: client not fetching req timely")
		}
	}
}

func (n *fanoutNotifier) IsEmpty() bool {
	n.RLock()
	defer n.RUnlock()

	return len(n.receptacles) == 0
}

func getFanoutKey(ctx context.Context) (string, error) {
	key, ok := ctx.Value(fanoutKey).(string)
	if !ok {
		return "", errors.New("context has no custom fanout key value")
	}

	return key, nil
}

func (n *fanoutNotifier) Register(ctx context.Context) (chan struct{}, error) {
	n.Lock()
	defer n.Unlock()

	key, err := getFanoutKey(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to register")
	}

	if recep, ok := n.receptacles[key]; ok {
		general.Warningf("pap: power capping server: signal slot already registered")
		return recep.ch, nil
	}

	// chan of size 1 to decouple producer and consumer
	ctxChan := make(chan struct{}, 1)
	n.receptacles[key] = &receptacle{
		ctx: ctx,
		ch:  ctxChan,
	}
	return ctxChan, nil
}

func (n *fanoutNotifier) Unregister(ctx context.Context) error {
	n.Lock()
	defer n.Unlock()

	key, err := getFanoutKey(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to register")
	}

	if recep, ok := n.receptacles[key]; ok {
		close(recep.ch)
	}
	delete(n.receptacles, key)
	return nil
}
