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
	// resctrl group names roughly correspond to pool names used by qrm plugins
	// todo: consider to consolidate qrm pool names with this common consts set
	ResctrlGroupRoot      = "/"
	ResctrlGroupDedicated = "dedicated"
	ResctrlGroupSystem    = "system"
	ResctrlGroupMachine   = "machine"
	ResctrlGroupShare     = "share"
	ResctrlGroupReclaim   = "reclaim"

	// subgroup related
	ResctrlSubgroupSeparator            = "-"
	ResctrlShareSubgroupPrefix          = "share-"
	ResctrlObsoleteSharedSubgroupPrefix = "shared-"
)
