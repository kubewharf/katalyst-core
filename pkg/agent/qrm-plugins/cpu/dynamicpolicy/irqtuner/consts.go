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

package irqtuner

// irq tuning error level
const (
	IrqTuningInfo    int64 = 0
	IrqTuningWarning int64 = 1
	IrqTuningError   int64 = 2
	IrqTuningFatal   int64 = 3
)

// irq tuning error reasons
const (
	NewIrqTuningControllerFailed                            string = "NewIrqTuningControllerFailed"
	SyncNicFailed                                           string = "SyncNicFailed"
	SyncContainersFailed                                    string = "SyncContainersFailed"
	TuneIrqAffinityForAllNicsWithBalanceFairPolicyFailed    string = "TuneIrqAffinityForAllNicsWithBalanceFairPolicyFailed"
	GetCurrentTotalExclusiveIrqCoresFailed                  string = "GetCurrentTotalExclusiveIrqCoresFailed"
	SetExclusiveIRQCPUSetFailed                             string = "SetExclusiveIRQCPUSetFailed"
	SetRPSForNicFailed                                      string = "SetRPSForNicFailed"
	ClearRPSForNicFailed                                    string = "ClearRPSForNicFailed"
	AdjustKsoftirqdsNiceFailed                              string = "AdjustKsoftirqdsNiceFailed"
	RestoreNicsOriginalIrqCoresExclusivePolicyFailed        string = "RestoreNicsOriginalIrqCoresExclusivePolicyFailed"
	UpdateIndicatorsStatsFailed                             string = "UpdateIndicatorsStatsFailed"
	CalculateNicExclusiveIrqCoresIncreaseFailed             string = "CalculateNicExclusiveIrqCoresIncreaseFailed"
	CalculateNicIrqCoresWhenSwitchToIrqCoresExclusiveFailed string = "CalculateNicIrqCoresWhenSwitchToIrqCoresExclusiveFailed"
	SelectExclusiveIrqCoresForNicFailed                     string = "SelectExclusiveIrqCoresForNicFailed"
	CalculateNicExclusiveIrqCoresDecreaseFailed             string = "CalculateNicExclusiveIrqCoresDecreaseFailed"
	TuneNicIrqAffinityWithBalanceFairPolicyFailed           string = "TuneNicIrqAffinityWithBalanceFairPolicyFailed"
	BalanceIrqsToOtherExclusiveIrqCoresFailed               string = "BalanceIrqsToOtherExclusiveIrqCoresFailed"
	TuneNicIrqsAffinityQualifiedCoresFailed                 string = "TuneNicIrqsAffinityQualifiedCoresFailed"
	BalanceNicIrqsToNewIrqCoresFailed                       string = "BalanceNicIrqsToNewIrqCoresFailed"
	TuneNicIrqAffinityPolicyToIrqCoresExclusiveFailed       string = "TuneNicIrqAffinityPolicyToIrqCoresExclusiveFailed"
)
