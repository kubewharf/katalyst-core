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
	"github.com/kubewharf/katalyst-core/pkg/controller/monitor"
)

const (
	MonitorControllerName = "monitor"
)

func StartMonitorController(ctx context.Context, controlCtx *katalystbase.GenericContext,
	conf *config.Configuration, _ interface{}, _ string) (bool, error) {
	var (
		cnrMonitorController *monitor.CNRMonitorController
		err                  error
	)

	if conf.MonitorConfig.EnableCNRMonitor {
		cnrMonitorController, err = monitor.NewCNRMonitorController(ctx,
			conf.GenericConfiguration,
			conf.GenericControllerConfiguration,
			conf.CNRMonitorConfig,
			controlCtx.Client,
			controlCtx.KubeInformerFactory.Core().V1().Nodes(),
			controlCtx.KubeInformerFactory.Core().V1().Pods(),
			controlCtx.InternalInformerFactory.Node().V1alpha1().CustomNodeResources(),
			controlCtx.EmitterPool.GetDefaultMetricsEmitter(),
		)
		if err != nil {
			klog.Errorf("failed to new CNR monitor controller")
			return false, err
		}
	}

	if cnrMonitorController != nil {
		go cnrMonitorController.Run()
	}

	return true, nil
}
