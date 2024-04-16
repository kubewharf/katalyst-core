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

package dirtymem

const EnableSetDirtyMemPeriodicalHandlerName = "SetDirtyMem"

const (
	sysDiskPrefix = "/sys/block"
	wbtSuffix     = "queue/wbt_lat_usec"
	mem2G         = 2147483648.0
)

const (
	metricNameDiskWBT = "async_handler_disk_wbt"
)
