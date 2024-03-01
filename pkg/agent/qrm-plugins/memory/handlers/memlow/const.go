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

package memlow

const EnableSetMemLowPeriodicalHandlerName = "SetCGMemLow"

const (
	// Constants for cgroup memory statistics
	cgroupMemory32M       = 33554432
	cgroupMemoryUnlimited = 9223372036854771712

	controlKnobKeyMemLow = "mem_low"
)

const (
	metricNameMemLow = "async_handler_cgroup_memlow"
)
