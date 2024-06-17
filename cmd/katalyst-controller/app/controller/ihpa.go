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
	"fmt"

	"k8s.io/klog/v2"

	katalyst "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/controller/ihpa"
)

const (
	IHPAControllerName = "ihpa"
)

func StartIHPAController(ctx context.Context, controlCtx *katalyst.GenericContext,
	conf *config.Configuration, extraConf interface{}, _ string,
) (bool, error) {
	if controlCtx == nil || conf == nil {
		err := fmt.Errorf("controlCtx and controllerConf can't be nil")
		klog.Error(err.Error())
		return false, err
	}

	ihpaController, err := ihpa.NewIHPAController(ctx,
		controlCtx,
		conf.GenericConfiguration,
		conf.GenericControllerConfiguration,
		conf.ControllersConfiguration.IHPAConfig,
		conf.QoSConfiguration,
		extraConf)
	if err != nil {
		klog.Errorf("failed to new ihpa controller")
		return false, err
	}

	go ihpaController.Run()
	return true, nil
}
