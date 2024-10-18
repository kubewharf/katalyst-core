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

package resctrl

import (
	"path"

	"github.com/spf13/afero"

	resctrlconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type SchemataUpdater interface {
	UpdateSchemata(ctrlGroup string, instruction string) error
}

type ccdMBSetter struct {
	fs afero.Fs
}

func updateSchemata(fs afero.Fs, ctrlGroup string, instruction string) error {
	schemataPath := path.Join(ctrlGroup, resctrlconsts.SchemataFile)
	general.InfofV(6, "mbm: writing to schemata %s with content %q", schemataPath, instruction)
	return afero.WriteFile(fs, schemataPath, []byte(instruction), resctrlconsts.FilePerm)
}

func (c ccdMBSetter) UpdateSchemata(ctrlGroup string, instruction string) error {
	return updateSchemata(c.fs, ctrlGroup, instruction)
}

func NewSchemataUpdater() (SchemataUpdater, error) {
	return &ccdMBSetter{
		fs: afero.NewOsFs(),
	}, nil
}
