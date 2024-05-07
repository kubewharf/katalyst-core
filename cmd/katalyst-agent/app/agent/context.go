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

package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.uber.org/atomic"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/config"
	"k8s.io/kubernetes/pkg/kubelet/pluginmanager"
	"k8s.io/kubernetes/pkg/kubelet/pluginmanager/cache"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	katalystconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
)

// InitFunc is used to construct the framework of agent component; all components
// should be initialized before any component starts to run, to make sure the
// dependencies are well handled before the running logic starts.
// the returned parameters are consisted of several parts
// - bool indicates whether the agent component needs to be started by calling Run function
// - Component indicates the corresponding component if starting is needed
// - error indicates whether the agent is succeeded to be initialized
type InitFunc func(agentCtx *GenericContext, conf *katalystconfig.Configuration,
	extraConf interface{}, agentName string) (bool, Component, error)

// Component is used as a common abstraction for all agent components.
type Component interface {
	// Run performs those running logic for each agent component
	// Run works in a blocking way, and all Component should wait
	// until the given ctx is done
	Run(ctx context.Context)
}

// ComponentStub is used as testing implementation for agent Component.
type ComponentStub struct{}

func (c ComponentStub) Run(_ context.Context) {}

var _ Component = ComponentStub{}

type PluginWrapper struct {
	skeleton.GenericPlugin
}

func (p *PluginWrapper) Run(ctx context.Context) {
	if err := p.Start(); err != nil {
		klog.Fatalf("start %v failed: %v", p.Name(), err)
	}

	klog.Infof("plugin wrapper %s started", p.Name())
	<-ctx.Done()
	if err := p.Stop(); err != nil {
		klog.Errorf("stop %v failed: %v", p.Name(), err)
	}
}

// GenericContext is used by katalyst-agent, and it's constructed based on
// unified katalyst-genetic context.
type GenericContext struct {
	*katalystbase.GenericContext

	// those are shared among other agent components
	*metaserver.MetaServer
	pluginmanager.PluginManager
}

func NewGenericContext(base *katalystbase.GenericContext, conf *katalystconfig.Configuration) (*GenericContext, error) {
	metaServer, err := metaserver.NewMetaServer(base.Client, base.EmitterPool.GetDefaultMetricsEmitter(), conf)
	if err != nil {
		return nil, fmt.Errorf("failed init meta server: %s", err)
	}

	pluginMgr, err := newPluginManager(conf)
	if err != nil {
		return nil, fmt.Errorf("failed init plugin manager: %s", err)
	}

	return &GenericContext{
		GenericContext: base,
		MetaServer:     metaServer,
		PluginManager:  pluginMgr,
	}, nil
}

func (c *GenericContext) Run(ctx context.Context) {
	go c.GenericContext.Run(ctx)
	go c.PluginManager.Run(config.NewSourcesReady(func(_ sets.String) bool { return true }), ctx.Done())
	go c.MetaServer.Run(ctx)
	<-ctx.Done()
}

// customizedPluginManager requires that handlers must be added before
// it really runs to avoid logging error logs too frequently
type customizedPluginManager struct {
	enabled atomic.Bool
	pluginmanager.PluginManager
}

// Run successes iff at-least-one handler has been registered
func (p *customizedPluginManager) Run(sourcesReady config.SourcesReady, stopCh <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	for ; true; <-ticker.C {
		if p.enabled.Load() {
			break
		}
		klog.V(5).Infof("skip starting plugin-manager without handlers added")
	}
	ticker.Stop()

	klog.Infof("started plugin-manager since handlers have been enabled")
	p.PluginManager.Run(sourcesReady, stopCh)
}

func (p *customizedPluginManager) AddHandler(pluginType string, pluginHandler cache.PluginHandler) {
	p.enabled.Store(true)
	p.PluginManager.AddHandler(pluginType, pluginHandler)
}

// newPluginManager initializes the registration logic for extendable plugins.
// all plugin manager added to generic context must use the same socket and
// default checkpoint path, and if some plugin needs to use a different socket path,
// it should create the plugin manager itself.
func newPluginManager(conf *katalystconfig.Configuration) (pluginmanager.PluginManager, error) {
	// make sure plugin registration directory already exist
	err := os.MkdirAll(conf.PluginRegistrationDir, os.FileMode(0o755))
	if err != nil {
		return nil, fmt.Errorf("initializes plugin registration dir failed: %s", err)
	}

	return &customizedPluginManager{
		enabled: *atomic.NewBool(false),
		PluginManager: pluginmanager.NewPluginManager(
			conf.PluginRegistrationDir, /* sockDir */
			&record.FakeRecorder{},
		),
	}, nil
}
