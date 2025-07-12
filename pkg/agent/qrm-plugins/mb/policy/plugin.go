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

package policy

import (
	"time"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	name     = "qrm_mb_plugin_generic_policy"
	interval = time.Second * 1
)

type MBPlugin struct {
	chStop chan struct{}
}

func (m *MBPlugin) Name() string {
	return name
}

func (m *MBPlugin) Start() error {
	general.Infof("mbm: plugin started")
	m.chStop = make(chan struct{})
	go wait.Until(m.run, interval, m.chStop)
	return nil
}

func (m *MBPlugin) Stop() error {
	general.Infof("mbm: plugin stopped")
	close(m.chStop)
	return nil
}

func (m *MBPlugin) run() {
	general.InfofV(6, "mbm: plugin run")
	// todo
}

func newMBPlugin() skeleton.GenericPlugin {
	return &MBPlugin{}
}
