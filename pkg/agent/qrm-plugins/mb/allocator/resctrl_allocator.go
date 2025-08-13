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
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/plan"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	fsRoot           = "/sys/fs/resctrl/"
	fileSchemata     = "schemata"
	filePermSchemata = 0o644
)

type resctrlAllocator struct {
	fs afero.Fs
}

func validatePath(name string) error {
	switch {
	case len(name) == 0:
		return errors.New("invalid empty name")
	case strings.ContainsRune(name, '/'):
		return fmt.Errorf("invalid path %s", name)
	default:
		return nil
	}
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
