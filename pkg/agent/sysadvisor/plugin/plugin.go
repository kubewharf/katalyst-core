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

// Package plugin is the package that defines the SysAdvisor plugin, and
// those strategies must implement SysAdvisorPlugin interface.

package plugin

import (
	"context"
)

// SysAdvisorPlugin performs resource control computation based on agent resources.
type SysAdvisorPlugin interface {
	Name() string
	Init() error
	Run(ctx context.Context)
}

var _ SysAdvisorPlugin = DummySysAdvisorPlugin{}

type DummySysAdvisorPlugin struct{}

func (d DummySysAdvisorPlugin) Name() string          { return "dummy-sysadvisor-plugin" }
func (d DummySysAdvisorPlugin) Init() error           { return nil }
func (d DummySysAdvisorPlugin) Run(_ context.Context) {}
