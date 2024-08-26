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

package qrm

type NetworkGroup struct {
	NetClassIDs []string `json:"net_class_ids"`
	Egress      uint32   `json:"egress"`
	Ingress     uint32   `json:"ingress"`
	MergedIPv4  string   `json:"merged_ipv4"`
	MergedIPv6  string   `json:"merged_ipv6"`
}
