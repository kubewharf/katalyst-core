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
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/node"
	"github.com/kubewharf/katalyst-core/pkg/controller/overcommit/prediction"
)

const (
	OvercommitControllerName = "overcommit"
)

func StartOvercommitController(
	ctx context.Context,
	controlCtx *katalyst.GenericContext,
	conf *config.Configuration,
	_ interface{},
	_ string,
) (bool, error) {
	noc, err := node.NewNodeOvercommitController(
		ctx,
		controlCtx,
		conf.GenericConfiguration,
		conf.ControllersConfiguration.OvercommitConfig,
	)
	if err != nil {
		klog.Errorf("failed to new nodeOvercommit controller")
		return false, err
	}
	go noc.Run()

	if conf.Prediction.EnablePredict {
		pc, err := prediction.NewPredictionController(
			ctx,
			controlCtx,
			conf.ControllersConfiguration.OvercommitConfig,
		)
		if err != nil {
			klog.Errorf("failed to new overcommit prediction controller")
			return false, err
		}

		go pc.Run()
	}
	return true, nil
}
