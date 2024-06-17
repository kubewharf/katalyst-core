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

package app

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/controller"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

// StartFunc is used to launch a particular controller.
// It may run additional "should I activate checks".
// Any error returned will cause the controller process to `Fatal`
// The bool indicates whether the controller was enabled.
type StartFunc func(ctx context.Context, controlCtx *katalystbase.GenericContext, conf *config.Configuration,
	extraControllerConf interface{}, controllerName string) (bool, error)

// ControllerStarter is used to start katalyst controllers
type ControllerStarter struct {
	Starter   StartFunc
	ExtraConf interface{}
}

// ControllersDisabledByDefault is the set of controllers which is disabled by default
var ControllersDisabledByDefault = sets.NewString()

// ControllerInitializers is used to store the initializing function for each controller
var controllerInitializers sync.Map

func init() {
	controllerInitializers.Store(controller.IHPAControllerName, ControllerStarter{Starter: controller.StartIHPAController})
	controllerInitializers.Store(controller.VPAControllerName, ControllerStarter{Starter: controller.StartVPAController})
	controllerInitializers.Store(controller.KCCControllerName, ControllerStarter{Starter: controller.StartKCCController})
	controllerInitializers.Store(controller.SPDControllerName, ControllerStarter{Starter: controller.StartSPDController})
	controllerInitializers.Store(controller.LifeCycleControllerName, ControllerStarter{Starter: controller.StartLifeCycleController})
	controllerInitializers.Store(controller.MonitorControllerName, ControllerStarter{Starter: controller.StartMonitorController})
	controllerInitializers.Store(controller.OvercommitControllerName, ControllerStarter{Starter: controller.StartOvercommitController})
	controllerInitializers.Store(controller.TideControllerName, ControllerStarter{Starter: controller.StartTideController})
	controllerInitializers.Store(controller.ResourceRecommenderControllerName, ControllerStarter{Starter: controller.StartResourceRecommenderController})
}

// RegisterControllerInitializer is used to register user-defined controllers
func RegisterControllerInitializer(name string, starter ControllerStarter) {
	controllerInitializers.Store(name, starter)
}

// GetControllerInitializers returns those controllers with initialized functions
func GetControllerInitializers() map[string]ControllerStarter {
	controllers := make(map[string]ControllerStarter)
	controllerInitializers.Range(func(key, value interface{}) bool {
		controllers[key.(string)] = value.(ControllerStarter)
		return true
	})
	return controllers
}
