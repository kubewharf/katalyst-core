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
	"fmt"
	"path"
	"strings"

	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
	resctrlconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
)

func GetResctrlSubMonGroups(fs afero.Fs, ctrlGroup string) ([]string, error) {
	result := make([]string, 0)
	dir := path.Join(resctrlconsts.FsRoot, ctrlGroup, resctrlconsts.SubGroupMonRoot)
	fis, err := afero.ReadDir(fs, dir)
	if err != nil {
		return nil, err
	}

	for _, fi := range fis {
		if !fi.IsDir() {
			continue
		}
		monGroup := fi.Name()
		result = append(result, path.Join(dir, monGroup))
	}

	return result, nil
}

func GetResctrlMonGroups(fs afero.Fs) ([]string, error) {
	ctlGroups, err := GetResctrlCtrlGroups(fs)
	if err != nil {
		return nil, err
	}

	result := make([]string, 0)
	for _, qosLevel := range ctlGroups {
		monGroupPaths, err := GetResctrlSubMonGroups(fs, qosLevel)
		if err != nil {
			return nil, err
		}
		result = append(result, monGroupPaths...)
	}
	return result, nil
}

func GetResctrlCtrlGroups(fs afero.Fs) ([]string, error) {
	result := make([]string, 0)
	fis, err := afero.ReadDir(fs, resctrlconsts.FsRoot)
	if err != nil {
		return nil, err
	}

	for _, fi := range fis {
		if !fi.IsDir() {
			continue
		}
		baseName := fi.Name()
		if !resctrlconsts.IsTopCtrlGroup(baseName) {
			continue
		}
		result = append(result, baseName)
	}
	return result, nil
}

func ParseMonGroup(path string) (qosGroup qosgroup.QoSGroup, podUID string, err error) {
	stem := strings.TrimPrefix(path, resctrlconsts.FsRoot)
	stem = strings.Trim(stem, "/")
	segs := strings.Split(stem, "/")
	if len(segs) != 3 {
		return "", "", fmt.Errorf("invalid mon group path: %s", path)
	}

	ctrlGroup, monGroup := segs[0], segs[2]
	qosGroup = qosgroup.QoSGroup(ctrlGroup)
	podUID = monGroup
	return qosGroup, podUID, nil
}
