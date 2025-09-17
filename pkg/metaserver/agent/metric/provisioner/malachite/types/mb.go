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
	Status int        `json:"status"`
	Data   MBRespData `json:"data"`
}

type MBRespData struct {
	MBData MBData `json:"resctrl"`
}

type MBData struct {
	// todo： group x ccd-mb as resctrl groups are the keys for qos
	// todo: process write victim if applicable
	MBBody     MBGroupData `json:"group_mbm"`
	UpdateTime int64       `json:"update_time"`
}

type MBGroupData map[string][]MBCCDStat

type MBCCDStat struct {
	CCDID          int    `json:"id"`
	MBLocalCounter uint64 `json:"mbm_local_bytes"`
	MBTotalCounter uint64 `json:"mbm_total_bytes"`
}
