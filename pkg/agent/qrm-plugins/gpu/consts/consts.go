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

package consts

import (
	"time"

	"github.com/kubewharf/katalyst-api/pkg/consts"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
)

const (

	// GPUResourcePluginPolicyNameStatic is the policy name of static gpu resource plugin
	GPUResourcePluginPolicyNameStatic = string(consts.ResourcePluginPolicyNameStatic)

	GPUPluginDynamicPolicyName = qrm.QRMPluginNameGPU + "_" + GPUResourcePluginPolicyNameStatic
	ClearResidualState         = GPUPluginDynamicPolicyName + "_clear_residual_state"

	StateCheckPeriod          = 30 * time.Second
	StateCheckTolerationTimes = 3
	MaxResidualTime           = 5 * time.Minute
)
