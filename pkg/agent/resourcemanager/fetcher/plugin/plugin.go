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

// Package plugin is the package that defines the reporter plugin, and
// those strategies must implement ReporterPlugin interface, supporting both
// embedded plugin and external registered plugin.
package plugin // import "github.com/kubewharf/katalyst-core/pkg/resourcemanager/fetcher/plugin"

import (
	"context"

	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	util "github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	fakePluginName = "fake-reporter-plugin"
)

type InitFunc func(emitter metrics.MetricEmitter, _ *metaserver.MetaServer, conf *config.Configuration, callback ListAndWatchCallback) (ReporterPlugin, error)

// ReporterPlugin performs report actions based on system or kubelet information.
type ReporterPlugin interface {
	Name() string
	Endpoint
}

type DummyReporterPlugin struct {
	*util.StopControl
}

func (_ DummyReporterPlugin) Name() string            { return fakePluginName }
func (_ DummyReporterPlugin) Run(success chan<- bool) {}
func (_ DummyReporterPlugin) GetReportContent(c context.Context) (*v1alpha1.GetReportContentResponse, error) {
	return nil, nil
}

func (_ DummyReporterPlugin) ListAndWatchReportContentCallback(s string, response *v1alpha1.GetReportContentResponse) {
}

func (_ DummyReporterPlugin) GetCache() *v1alpha1.GetReportContentResponse {
	return nil
}

var _ ReporterPlugin = DummyReporterPlugin{}
