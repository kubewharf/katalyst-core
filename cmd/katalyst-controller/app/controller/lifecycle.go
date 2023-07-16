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

package controller

import (
	"context"

	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/controller/lifecycle"
	agent_healthz "github.com/kubewharf/katalyst-core/pkg/controller/lifecycle/agent-healthz"
)

const (
	LifeCycleControllerName = "lifecycle"
)

func StartLifeCycleController(ctx context.Context, controlCtx *katalystbase.GenericContext,
	conf *config.Configuration, _ interface{}, _ string) (bool, error) {
	var (
		cnrLifecycle *lifecycle.CNRLifecycle
		cncLifecycle *lifecycle.CNCLifecycle
		eviction     *agent_healthz.HealthzController
		err          error
	)

	cnrLifecycle, err = lifecycle.NewCNRLifecycle(ctx,
		conf.GenericConfiguration,
		conf.GenericControllerConfiguration,
		conf.ControllersConfiguration.CNRLifecycleConfig,
		controlCtx.Client,
		controlCtx.KubeInformerFactory.Core().V1().Nodes(),
		controlCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources(),
		controlCtx.EmitterPool.GetDefaultMetricsEmitter(),
	)
	if err != nil {
		klog.Errorf("failed to new CNR lifecycle controller")
		return false, err
	}

	if conf.LifeCycleConfig.EnableCNCLifecycle {
		cncLifecycle, err = lifecycle.NewCNCLifecycle(ctx,
			conf.GenericConfiguration,
			conf.GenericControllerConfiguration,
			conf.ControllersConfiguration.CNCLifecycleConfig,
			controlCtx.Client,
			controlCtx.KubeInformerFactory.Core().V1().Nodes(),
			controlCtx.InternalInformerFactory.Config().V1alpha1().CustomNodeConfigs(),
			controlCtx.EmitterPool.GetDefaultMetricsEmitter(),
		)
		if err != nil {
			klog.Errorf("failed to new CNC lifecycle controller")
			return false, err
		}
	}

	if conf.LifeCycleConfig.EnableHealthz {
		eviction, err = agent_healthz.NewHealthzController(ctx,
			conf.GenericConfiguration,
			conf.GenericControllerConfiguration,
			conf.ControllersConfiguration.LifeCycleConfig,
			controlCtx.Client,
			controlCtx.KubeInformerFactory.Core().V1().Nodes(),
			controlCtx.KubeInformerFactory.Core().V1().Pods(),
			controlCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources(),
			controlCtx.EmitterPool.GetDefaultMetricsEmitter(),
		)
		if err != nil {
			klog.Errorf("failed to new eviction controller")
			return false, err
		}
	}

	if cnrLifecycle != nil {
		go cnrLifecycle.Run()
	}

	if cncLifecycle != nil {
		go cncLifecycle.Run()
	}

	if eviction != nil {
		go eviction.Run()
	}

	return true, nil
}
