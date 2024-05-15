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

	katalyst "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/controller/vpa"
)

const (
	VPAControllerName = "vpa"
)

func StartVPAController(ctx context.Context, controlCtx *katalyst.GenericContext,
	conf *config.Configuration, _ interface{}, _ string,
) (bool, error) {
	r, err := vpa.NewResourceRecommendController(ctx, controlCtx,
		conf.GenericConfiguration,
		conf.GenericControllerConfiguration,
		conf.ControllersConfiguration.VPAConfig)
	if err != nil {
		klog.Errorf("failed to new resource recommend controller")
		return false, err
	}

	vr, err := vpa.NewVPARecommendationController(ctx, controlCtx,
		conf.GenericConfiguration,
		conf.GenericControllerConfiguration,
		conf.ControllersConfiguration.VPAConfig)
	if err != nil {
		klog.Errorf("failed to new vpa recommendatin controller")
		return false, err
	}

	v, err := vpa.NewVPAController(ctx, controlCtx,
		conf.GenericConfiguration,
		conf.GenericControllerConfiguration,
		conf.ControllersConfiguration.VPAConfig)
	if err != nil {
		klog.Errorf("failed to new vpa controller")
		return false, err
	}

	go r.Run()
	go v.Run()
	go vr.Run()
	return true, nil
}
