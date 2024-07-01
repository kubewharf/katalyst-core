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

package mbm

import (
	"errors"

	"github.com/kubewharf/katalyst-core/pkg/mbw/monitor"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/mbw"
)

type MBAdjuster interface {
	AdjustNumaMB(node int, avgMB, quota uint64, action monitor.MB_CONTROL_ACTION) error
}

type dummyMBAdjuster struct{}

func (d dummyMBAdjuster) AdjustNumaMB(node int, avgMB, quota uint64, action monitor.MB_CONTROL_ACTION) error {
	return errors.New("low level mbw not enabled")
}

func NewMBAdjuster() MBAdjuster {
	if adjuster := mbw.GetMBWMonitor(); adjuster != nil {
		return adjuster
	}

	return &dummyMBAdjuster{}
}
