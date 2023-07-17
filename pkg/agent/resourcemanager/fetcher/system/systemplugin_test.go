//go:build linux
// +build linux

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

package system

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func Test_systemPlugin_GetReportContent(t *testing.T) {
	t.Parallel()

	genericClient := &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(),
		InternalClient: internalfake.NewSimpleClientset(),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
	}
	conf := generateTestConfiguration(t)
	meta, err := metaserver.NewMetaServer(genericClient, metrics.DummyMetrics{}, conf)
	assert.NoError(t, err)

	plugin, err := NewSystemReporterPlugin(metrics.DummyMetrics{}, meta, conf, nil)
	assert.NoError(t, err)

	content, err := plugin.GetReportContent(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, content)
}
