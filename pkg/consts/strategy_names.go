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

const (
	// StrategyNameBorweinV2 is the name of borwein_v2 strategy,
	// it adjusts the amount of be headroom on node in real time based on model inference results
	StrategyNameBorweinV2 = "borwein_v2"
	// StrategyNameBalanceSchedV1 is the name of balance_sched_v1 strategy,
	// it scatters the overloaded nodes by pre-set node utilization and load thresholds
	StrategyNameBalanceSchedV1 = "balance_sched_v1"
	// StrategyNameBorweinOfflineTraining is the name of borwein_offline_training strategy,
	// it reschedules offline training pods according to model inference results to improve the performance of the pods.
	StrategyNameBorweinOfflineTraining = "borwein_offline_training"
	// StrategyNameBorweinTaint is the name of borwein_taint strategy,
	// it taints nodes with poor performance according to model inference results.
	StrategyNameBorweinTaint = "borwein_taint"
)

const (
	StrategyNameSpliter = ","
)
