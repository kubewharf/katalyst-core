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

package mbm

type MB_CONTROL_ACTION int

const (
	MEMORY_BANDWIDTH_CONTROL_RAISE      MB_CONTROL_ACTION = 1
	MEMORY_BANDWIDTH_CONTROL_REDUCE     MB_CONTROL_ACTION = 2
	MEMORY_BANDWIDTH_CONTROL_UNTHROTTLE MB_CONTROL_ACTION = 3
)

type MBAdjuster interface {
	AdjustNumaMB(node int, avgMB, quota uint64, action MB_CONTROL_ACTION) error
}
