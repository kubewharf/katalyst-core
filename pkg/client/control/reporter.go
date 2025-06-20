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

package control

import (
	"context"
	"sync"

	"github.com/pkg/errors"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type Reporter interface {
	// ReportContents reports the given contents to the Katalyst server.
	// It will be pushed to a queue and sent by the reporter in a batch manner.
	// If fastPush is true, the contents will be pushed to the head of the queue and sent as soon as possible.
	ReportContents(ctx context.Context, contents []*v1alpha1.ReportContent, fastPush bool) error
	// Run starts the reporter to send contents in the queue to the Katalyst server periodically.
	Run(ctx context.Context) error
}

type DummyReporter struct{}

func (d DummyReporter) ReportContents(_ context.Context, _ []*v1alpha1.ReportContent, _ bool) error {
	return nil
}

func (d DummyReporter) Run(_ context.Context) error {
	return nil
}

type genericReporterPlugin struct {
	sync.RWMutex
	started bool
	stop    chan struct{}
	name    string

	restartFunc func() error
	startFunc   func() error
	stopFunc    func() error

	resp       *v1alpha1.GetReportContentResponse
	notifierCh chan struct{}
}

// NewGenericReporterPlugin creates a new generic reporter plugin.
// It wraps the plugin with a registration mechanism, and sets up start, stop, and restart functionalities.
func NewGenericReporterPlugin(name string, conf *config.Configuration, emitter metrics.MetricEmitter) (Reporter, error) {
	c := &genericReporterPlugin{
		name: name, // Assign the provided name to the plugin instance.
	}

	rp, err := skeleton.NewRegistrationPluginWrapper(c, []string{conf.PluginRegistrationDir}, func(key string, value int64) {
		_ = emitter.StoreInt64(key, value, metrics.MetricTypeNameCount, metrics.ConvertMapToTags(map[string]string{
			"pluginName": name,
			"pluginType": registration.ReporterPlugin,
		})...)
	})
	if err != nil {
		return nil, err
	}

	c.restartFunc = rp.Restart
	c.startFunc = rp.Start
	c.stopFunc = rp.Stop

	return c, nil
}

// Run starts the reporter plugin and blocks until the context is done.
// It manages the plugin's lifecycle through its start and stop functions.
func (c *genericReporterPlugin) Run(ctx context.Context) error {
	if err := c.startFunc(); err != nil {
		return err
	}

	klog.Infof("plugin %s started", c.name)
	<-ctx.Done()
	if err := c.stopFunc(); err != nil {
		klog.Errorf("stop %v failed: %v", c.name, err)
	}
	return nil
}

// ReportContents updates the reported content. If fastPush is true and the content has changed,
// it notifies listeners. If fastPush is true and the content is the same as the last reported content,
// it attempts to send a notification through notifierCh, which is typically listened to by ListAndWatchReportContent.
func (c *genericReporterPlugin) ReportContents(ctx context.Context, contents []*v1alpha1.ReportContent, fastPush bool) error {
	resp := &v1alpha1.GetReportContentResponse{
		Content: contents,
	}

	c.Lock()
	lastResp := c.resp
	c.resp = resp
	ch := c.notifierCh
	c.Unlock()

	if fastPush && !apiequality.Semantic.DeepEqual(lastResp, resp) && ch != nil {
		for {
			select {
			case <-ctx.Done():
				general.Infof("plugin %s force push failed due to context cancellation", c.name)
				return ctx.Err()
			case ch <- struct{}{}:
				general.Infof("plugin %s force pushed notification", c.name)
				return nil
			}
		}
	}

	return nil
}

// Name returns the name of the plugin.
func (c *genericReporterPlugin) Name() string {
	return c.name
}

func (c *genericReporterPlugin) Start() (err error) {
	c.Lock()
	defer func() {
		if err == nil {
			c.started = true
		}
		c.Unlock()
	}()

	if c.started {
		return
	}

	c.stop = make(chan struct{})
	return nil
}

// Stop halts the plugin, cleans up its resources, and marks it as not started.
// This method is idempotent, meaning it's safe to call multiple times or on a plugin that hasn't been started.
func (c *genericReporterPlugin) Stop() error {
	c.Lock()
	defer func() {
		c.started = false
		c.Unlock()
	}()

	// plugin.Stop may be called before plugin.Start or multiple times,
	// we should ensure cancel function exists
	if !c.started {
		return nil
	}

	close(c.stop)
	return nil
}

// GetReportContent is the gRPC handler for retrieving the current report content.
// It implements the skeleton.ReporterPlugin interface.
func (c *genericReporterPlugin) GetReportContent(_ context.Context, _ *v1alpha1.Empty) (*v1alpha1.GetReportContentResponse, error) {
	return c.getReportContent()
}

func (c *genericReporterPlugin) getReportContent() (*v1alpha1.GetReportContentResponse, error) {
	c.RLock()
	defer c.RUnlock()
	if c.started && c.resp != nil {
		return c.resp, nil
	}
	return nil, errors.New("plugin is not started or no content is available")
}

// ListAndWatchReportContent is the gRPC handler for streaming report content updates.
// It listens for notifications on notifierCh (triggered by ReportContents with fastPush enabled)
// or stop signals, and sends the current content to the client.
// It implements the skeleton.ReporterPlugin interface.
func (c *genericReporterPlugin) ListAndWatchReportContent(_ *v1alpha1.Empty, server v1alpha1.ReporterPlugin_ListAndWatchReportContentServer) error {
	ch := make(chan struct{}, 1)

	c.Lock()
	c.notifierCh = ch
	stop := c.stop
	c.Unlock()

	defer func() {
		c.Lock()
		c.notifierCh = nil
		c.Unlock()
		close(ch)
	}()

	if !c.started {
		return errors.New("plugin is not started")
	}

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				general.Infof("plugin %s notification channel closed, assuming stopped", c.name)
				return nil
			}

			resp, err := c.getReportContent()
			if err != nil {
				general.Errorf("plugin %s failed to get report content: %v", c.name, err)
				continue
			}

			err = server.Send(resp)
			if err != nil {
				general.Errorf("plugin %s failed to send report content with error %v", c.name, err)
				return c.restartFunc()
			}
		case <-stop:
			general.Warningf("plugin %s received stop signal", c.name)
			return nil
		case <-server.Context().Done():
			general.Infof("plugin %s gRPC stream context done, assuming stopped", c.name)
			return nil
		}
	}
}

var _ skeleton.ReporterPlugin = &genericReporterPlugin{}
