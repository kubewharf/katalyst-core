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

package allocator

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	fsRoot            = "/sys/fs/resctrl/"
	fileSchemata      = "schemata"
	filePermSchemata  = 0o644
	defaultCCDMBValue = 256_000
)

type resctrlAllocator struct {
	fs afero.Fs
}

func validatePath(name string) error {
	switch {
	case len(name) == 0:
		return errors.New("invalid empty name")
	case strings.ContainsRune(name, '/'):
		if name == "/" {
			return nil
		}
		return fmt.Errorf("invalid multi-folder path %s", name)
	default:
		return nil
	}
}

func (r *resctrlAllocator) getResctrlControlGroups() ([]string, error) {
	fileInfos, err := afero.ReadDir(r.fs, fsRoot)
	if err != nil {
		return nil, errors.Wrap(err, "failed to read resctrl directory")
	}

	result := []string{"/"}
	for _, fileInfo := range fileInfos {
		if fileInfo.IsDir() {
			subFolder := fileInfo.Name()
			schemataFile := filepath.Join(fsRoot, subFolder, fileSchemata)
			ok, _ := afero.Exists(r.fs, schemataFile)
			if ok {
				result = append(result, subFolder)
			}
		}
	}

	return result, nil
}

func getDefaultCCDPlan(ccds sets.Int) map[int]int {
	result := make(map[int]int)
	for ccd := range ccds {
		result[ccd] = defaultCCDMBValue
	}
	return result
}

func (r *resctrlAllocator) getResetPlan(ccds sets.Int) (*plan.MBPlan, error) {
	plan := plan.MBPlan{
		MBGroups: map[string]plan.GroupCCDPlan{},
	}
	groups, err := r.getResctrlControlGroups()
	if err != nil {
		return nil, errors.Wrap(err, "failed to get reset resctrl plan")
	}

	for _, group := range groups {
		plan.MBGroups[group] = getDefaultCCDPlan(ccds)
	}

	return &plan, nil
}

func (r *resctrlAllocator) Reset(ctx context.Context, ccds sets.Int) error {
	resetPlan, err := r.getResetPlan(ccds)
	if err != nil {
		return errors.Wrap(err, "failed to reset resctrl")
	}

	return r.Allocate(ctx, resetPlan)
}

func (r *resctrlAllocator) Allocate(ctx context.Context, plan *plan.MBPlan) error {
	for group, ccdPlan := range plan.MBGroups {
		if err := r.allocateGroupPlan(group, ccdPlan); err != nil {
			return errors.Wrap(err, "failed to allocate mb plan")
		}
	}
	return nil
}

func (r *resctrlAllocator) allocateGroupPlan(group string, ccdPlan plan.GroupCCDPlan) error {
	if err := validatePath(group); err != nil {
		return errors.Wrap(err, fmt.Sprintf("invalid group name %q", group))
	}

	schemataPath := path.Join(fsRoot, group, fileSchemata)
	if _, err := r.fs.Stat(schemataPath); err != nil {
		return errors.Wrap(err, fmt.Sprintf("unable to locate schemata file for group %s", group))
	}

	return r.updateSchemata(schemataPath, ccdPlan)
}

func (r *resctrlAllocator) updateSchemata(schemataPath string, ccdPlan plan.GroupCCDPlan) error {
	instruction := ccdPlan.ToSchemataInstruction()
	general.InfofV(6, "mbm: writing to schemata %s with content %q", schemataPath, instruction)
	return afero.WriteFile(r.fs, schemataPath, instruction, filePermSchemata)
}

func New() PlanAllocator {
	return &resctrlAllocator{
		fs: afero.NewOsFs(),
	}
}
