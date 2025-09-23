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

package types

// todo: revise after the payload format is finalized
type MalachiteMBResponse struct {
	Status int    `json:"status"`
	Data   MBData `json:"data"`
}

type MBData struct {
	MBBody map[string]MBGroupData `json:"mb"`
	// todo: support cached_write_mb option
	UpdateTime int64 `json:"update_time"`
}

type MBGroupData = map[int]MBStat

type MBStat struct {
	MBLocal  float64 `json:"local"`
	MBRemote float64 `json:"remote"`
}
