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

import "strings"

const (
	FsRoot          = "/sys/fs/resctrl"
	SubGroupMonRoot = "mon_groups"
	MonData         = "mon_data"

	GroupDedicated  = "dedicated"
	GroupSharedCore = "shared"
	GroupReclaimed  = "reclaimed"
	GroupSystem     = "system"

	FolderPerm = 0755
	FilePerm   = 0644

	TasksFile    = "tasks"
	MBRawFile    = "mbm_total_bytes"
	SchemataFile = "schemata"

	TmplTaskFolder   = "%s"
	TmplCCDMonFolder = "mon_L3_%02d"

	UninitializedMB = -1
)

func IsTopCtrlGroup(name string) bool {
	switch name {
	case GroupDedicated:
		return true
	case GroupSystem:
		return true
	case GroupReclaimed:
		return true
	default:
		return strings.HasPrefix(name, GroupSharedCore)
	}
}
