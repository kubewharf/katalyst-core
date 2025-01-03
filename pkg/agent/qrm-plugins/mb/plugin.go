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

package mb

import (
	"time"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/controller"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const pluginName = "qrm_mb_plugin"

// plugin is the memory bandwidth resource control plugin
type plugin struct {
	dieTopology        *machine.DieTopology
	incubationInterval time.Duration
	mbController       *controller.Controller
}

func (p *plugin) Name() string {
	return pluginName
}

func (p *plugin) Start() error {
	general.Infof("mbm: mb plugin is starting: incubation interval %v", p.incubationInterval)
	general.InfofV(6, "mbm: numa-CCD-cpu topology: \n%s", p.dieTopology)

	// todo: NOT to return error (to crash explicitly); consider downgrade service
	if !p.dieTopology.FakeNUMAEnabled {
		return errors.New("mbm: not virtual numa; no need to dynamically manage the memory bandwidth")
	}

	go func() {
		defer func() {
			err := recover()
			if err != nil {
				general.Errorf("mbm: background run exited, due to error: %v", err)
			}
		}()

		p.mbController.Run()
	}()

	return nil
}

func (p *plugin) Stop() error {
	general.Infof("mbm: mb plugin is stopping...")
	return p.mbController.Stop()
}
