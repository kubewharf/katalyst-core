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

package qrm

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

const QRMAsyncHandlers = "qrm_async_handlers"

// AsyncHandler defines interface for async-handlers for qrm-plugins, since some ControlKnobs
// can only be done in an async and bypass way instead of the main-loop of qrm-plugin.
type AsyncHandler func(ctx context.Context)

var handlerMtx sync.RWMutex
var handlerImps []AsyncHandler

// RegisterHandler registers a handler as seperated async goroutine
func RegisterHandler(h AsyncHandler) {
	handlerMtx.Lock()
	defer handlerMtx.Unlock()

	handlerImps = append(handlerImps, h)
}

// GetRegisterHandlers returns all the handlers that should be started
func getRegisterHandlers() []AsyncHandler {
	handlerMtx.RLock()
	defer handlerMtx.RUnlock()

	return handlerImps
}

// AsyncHandlerManager starts all the registered handlers
type AsyncHandlerManager struct{}

func InitAsyncHandlerManager(_ *agent.GenericContext, _ *config.Configuration,
	_ interface{}, _ string) (bool, agent.Component, error) {
	return true, &AsyncHandlerManager{}, nil
}

func (_ *AsyncHandlerManager) Run(ctx context.Context) {
	for _, h := range getRegisterHandlers() {
		go wait.Until(func() { h(ctx) }, time.Second*30, ctx.Done())
	}
}
