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
)

const (
	// NetworkResourcePluginPolicyNameDynamic is the name of the dynamic policy.
	NetworkResourcePluginPolicyNameDynamic = string(consts.ResourcePluginPolicyNameDynamic)

	NetworkPluginDynamicPolicyName = "qrm_network_plugin_" + NetworkResourcePluginPolicyNameDynamic
	ClearResidualState             = NetworkPluginDynamicPolicyName + "_clear_residual_state"

	StateCheckPeriod          = 30 * time.Second
	StateCheckTolerationTimes = 3
	MaxResidualTime           = 5 * time.Minute
)
