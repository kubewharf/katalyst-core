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
	"github.com/kubewharf/katalyst-core/pkg/controller/kcc"
	kcctarget "github.com/kubewharf/katalyst-core/pkg/controller/kcc/target"
)

const (
	KCCControllerName = "kcc"
)

func StartKCCController(ctx context.Context, controlCtx *katalystbase.GenericContext,
	conf *config.Configuration, _ interface{}, _ string,
) (bool, error) {
	// targetHandler is initialized once and shared by multiple controllers
	targetHandler := kcctarget.NewKatalystCustomConfigTargetHandler(
		ctx,
		controlCtx.Client,
		conf.ControllersConfiguration.KCCConfig,
		controlCtx.InternalInformerFactory.Config().V1alpha1().KatalystCustomConfigs(),
	)

	kccController, err := kcc.NewKatalystCustomConfigController(
		ctx,
		conf.GenericConfiguration,
		conf.GenericControllerConfiguration,
		conf.ControllersConfiguration.KCCConfig,
		controlCtx.Client,
		controlCtx.InternalInformerFactory.Config().V1alpha1().KatalystCustomConfigs(),
		controlCtx.EmitterPool.GetDefaultMetricsEmitter(),
		targetHandler,
	)
	if err != nil {
		klog.Errorf("failed to new kcc controller")
		return false, err
	}

	kccTargetController, err := kcc.NewKatalystCustomConfigTargetController(
		ctx,
		conf.GenericConfiguration,
		conf.GenericControllerConfiguration,
		conf.ControllersConfiguration.KCCConfig,
		controlCtx.Client,
		controlCtx.InternalInformerFactory.Config().V1alpha1().KatalystCustomConfigs(),
		controlCtx.InternalInformerFactory.Config().V1alpha1().CustomNodeConfigs(),
		controlCtx.EmitterPool.GetDefaultMetricsEmitter(),
		targetHandler,
	)
	if err != nil {
		klog.Errorf("failed to new kcc target controller")
		return false, err
	}

	go targetHandler.Run()
	go kccController.Run()
	go kccTargetController.Run()
	return true, nil
}
