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

package periodicalhandler

import (
	"context"
	"fmt"
	"reflect"
	"runtime"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type Handler func(coreConf *config.Configuration,
	extraConf interface{},
	dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer)

type HandlerCtx struct {
	handler  Handler
	ctx      context.Context
	cancel   context.CancelFunc
	interval time.Duration
	ready    bool
	funcName string
}

// PeriodicalHandlerManager works as a general framework to run periodical jobs;
// you can register those jobs and mark them as started or stopped, and the manager
// will periodically check and run/cancel according to the job expected status.
type PeriodicalHandlerManager struct {
	coreConf    *config.Configuration
	extraConf   interface{}
	dynamicConf *dynamicconfig.DynamicAgentConfiguration
	emitter     metrics.MetricEmitter
	metaServer  *metaserver.MetaServer
}

func NewPeriodicalHandlerManager(agentCtx *agent.GenericContext, coreConf *config.Configuration,
	extraConf interface{}, agentName string) (bool, agent.Component, error) {

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName)

	return true, &PeriodicalHandlerManager{
		coreConf:    coreConf,
		extraConf:   extraConf,
		emitter:     wrappedEmitter,
		metaServer:  agentCtx.MetaServer,
		dynamicConf: coreConf.DynamicAgentConfiguration,
	}, nil
}

func (phm *PeriodicalHandlerManager) Run(ctx context.Context) {
	wait.Until(func() {
		handlerMtx.Lock()
		defer handlerMtx.Unlock()

		for groupName, groupHandlerCtxs := range handlerCtxs {
			for handlerName := range groupHandlerCtxs {
				handlerCtx := groupHandlerCtxs[handlerName]
				if handlerCtx == nil {
					general.Warningf("nil handlerCtx")
					continue
				} else if handlerCtx.ctx != nil {
					continue
				} else if !handlerCtx.ready {
					general.InfoS("handler isn't ready",
						"groupName", groupName,
						"handlerName", handlerName,
						"funcName", handlerCtx.funcName,
						"interval", handlerCtx.interval)
					continue
				}

				general.InfoS("start handler",
					"groupName", groupName,
					"handlerName", handlerName,
					"funcName", handlerCtx.funcName,
					"interval", handlerCtx.interval)

				handlerCtx.ctx, handlerCtx.cancel = context.WithCancel(context.Background())
				go wait.Until(func() {
					handlerCtx.handler(phm.coreConf, phm.extraConf, phm.dynamicConf, phm.emitter, phm.metaServer)
				}, handlerCtx.interval, handlerCtx.ctx.Done())
			}
		}
	}, 5*time.Second, ctx.Done())
}

// the first key is the handlers group name
// the second key is the handler name
var handlerCtxs = make(map[string]map[string]*HandlerCtx)
var handlerMtx sync.Mutex

func RegisterPeriodicalHandler(groupName, handlerName string, handler Handler, interval time.Duration) (err error) {
	if groupName == "" || handlerName == "" {
		return fmt.Errorf("emptry groupName: %s or handlerName: %s", groupName, handlerName)
	} else if handler == nil {
		return fmt.Errorf("nil handler")
	} else if interval <= 0 {
		return fmt.Errorf("invalid interval: %v", interval)
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recover from: %v", r)
			return
		}
	}()

	newFuncName := runtime.FuncForPC(reflect.ValueOf(handler).Pointer()).Name()

	handlerMtx.Lock()
	defer handlerMtx.Unlock()

	if handlerCtxs[groupName][handlerName] != nil {
		general.InfoS("replace periodical handler",
			"groupName", groupName,
			"handlerName", handlerName,
			"oldFuncName", handlerCtxs[groupName][handlerName].funcName,
			"oldInterval", handlerCtxs[groupName][handlerName].interval,
			"newFuncName", newFuncName,
			"newInterval", interval)

		if handlerCtxs[groupName][handlerName].cancel != nil {
			handlerCtxs[groupName][handlerName].cancel()
		}
	} else {
		general.InfoS("add periodical handler",
			"groupName", groupName,
			"handlerName", handlerName,
			"newFuncName", newFuncName,
			"newInterval", interval)

		if handlerCtxs[groupName] == nil {
			handlerCtxs[groupName] = make(map[string]*HandlerCtx)
		}
	}

	handlerCtxs[groupName][handlerName] = &HandlerCtx{
		handler:  handler,
		interval: interval,
		funcName: newFuncName,
	}

	return nil
}

func ReadyToStartHandlersByGroup(groupName string) {
	handlerMtx.Lock()
	defer handlerMtx.Unlock()

	general.InfoS("called", "groupName", groupName)

	for handlerName, handlerCtx := range handlerCtxs[groupName] {
		if handlerCtx == nil {
			general.Warningf("nil handlerCtx")
			continue
		} else if handlerCtx.ctx != nil {
			general.InfoS("handler already started",
				"groupName", groupName,
				"handlerName", handlerName,
				"funcName", handlerCtx.funcName,
				"interval", handlerCtx.interval)
			continue
		}

		general.InfoS("handler is ready",
			"groupName", groupName,
			"handlerName", handlerName,
			"funcName", handlerCtx.funcName,
			"interval", handlerCtx.interval)

		handlerCtx.ready = true
	}
}

func StopHandlersByGroup(groupName string) {
	handlerMtx.Lock()
	defer handlerMtx.Unlock()

	general.InfoS("called", "groupName", groupName)

	for handlerName, handlerCtx := range handlerCtxs[groupName] {
		if handlerCtx == nil {
			general.Warningf("nil handlerCtx")
			continue
		} else if handlerCtx.ctx == nil {
			general.InfoS("handler already stopped",
				"groupName", groupName,
				"handlerName", handlerName,
				"funcName", handlerCtx.funcName,
				"interval", handlerCtx.interval)
			continue
		}

		general.InfoS("stop handler",
			"groupName", groupName,
			"handlerName", handlerName,
			"funcName", handlerCtx.funcName,
			"interval", handlerCtx.interval)

		handlerCtx.cancel()
		handlerCtx.cancel = nil
		handlerCtx.ctx = nil
		handlerCtx.ready = false
	}
}
