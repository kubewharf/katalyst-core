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

package file

import (
	"strconv"

	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
)

// ReadValueFromFile returns -1 as invalid MB is file content is not digits
func ReadValueFromFile(fs afero.Fs, path string) int64 {
	buffer, err := afero.ReadFile(fs, path)
	if err != nil {
		return consts.InvalidMB
	}

	if string(buffer) == "Unavailable" {
		return consts.InvalidMB
	}

	v, err := strconv.ParseInt(string(buffer), 10, 64)
	if err != nil {
		return consts.InvalidMB
	}

	return v
}
