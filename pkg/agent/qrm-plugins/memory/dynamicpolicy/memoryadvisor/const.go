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

package memoryadvisor

type MemoryControlKnobName string

const (
	ControlKnobKeyMemoryLimitInBytes MemoryControlKnobName = "memory_limit_in_bytes"
	ControlKnobKeyDropCache          MemoryControlKnobName = "drop_cache"
	ControlKnobKeyCPUSetMems         MemoryControlKnobName = "cpuset_mems"
	ControlKnobReclaimedMemorySize   MemoryControlKnobName = "reclaimed_memory_size"
	ControlKnobKeyBalanceNumaMemory  MemoryControlKnobName = "balance_numa_memory"
)
