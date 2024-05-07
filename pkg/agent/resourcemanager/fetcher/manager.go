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

// Package fetcher is a framework to collect resources from multiple plugins
// (both in-tree and out-of-tree implementations) and push contents to reporter
// manager to assemble and update thrugh APIServer.
package fetcher // import "github.com/kubewharf/katalyst-core/pkg/reportermanager/fetcher"

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	pluginapi "k8s.io/kubelet/pkg/apis/deviceplugin/v1beta1"
	"k8s.io/kubernetes/pkg/kubelet/checkpointmanager"
	cpmerrors "k8s.io/kubernetes/pkg/kubelet/checkpointmanager/errors"

	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/checkpoint"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/kubelet"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/plugin"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/fetcher/system"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/reporter"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const reporterManagerCheckpoint = "reporter_manager_checkpoint"

const (
	metricsNameGetContentCost       = "reporter_get_content_cost"
	metricsNameGetContentPluginCost = "reporter_get_content_plugin_cost"
	metricsNameGenericSyncCost      = "reporter_generic_sync_cost"
)

// ReporterPluginManager is used to manage in-tree or out-tree reporter plugin registrations and
// get report content from these plugins to aggregate them into the Reporter Manager
type ReporterPluginManager struct {
	// callback is used for reporting in one time call.
	callback plugin.ListAndWatchCallback

	// map pluginName to its corresponding endpoint implementation
	mutex          sync.Mutex
	innerEndpoints sets.String
	endpoints      map[string]plugin.Endpoint

	checkpointManager checkpointmanager.CheckpointManager

	reporter reporter.Manager
	emitter  metrics.MetricEmitter

	// reconcilePeriod is the duration between calls to sync.
	reconcilePeriod time.Duration
	syncFunc        func(ctx context.Context)

	// healthzState records last time that the corresponding module is determined as healthy.
	healthzState sync.Map
}

var innerReporterPluginsDisabledByDefault = sets.NewString()

// NewReporterPluginManager creates a new reporter plugin manager.
func NewReporterPluginManager(reporterMgr reporter.Manager, emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer, conf *config.Configuration,
) (*ReporterPluginManager, error) {
	manager := &ReporterPluginManager{
		innerEndpoints:  sets.NewString(),
		endpoints:       make(map[string]plugin.Endpoint),
		reporter:        reporterMgr,
		emitter:         emitter,
		reconcilePeriod: conf.CollectInterval,
	}

	manager.syncFunc = manager.genericSync
	manager.callback = manager.genericCallback

	checkpointManager, err := checkpointmanager.NewCheckpointManager(conf.CheckpointManagerDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize checkpoint manager: %v", err)
	}
	manager.checkpointManager = checkpointManager

	// load remote endpoints report response information from disk.
	err = manager.readCheckpoint()
	if err != nil {
		_ = emitter.StoreInt64("reporter_plugin_checkpoint_read_failed", 1, metrics.MetricTypeNameCount)
		klog.Warningf("continue after failing to read checkpoint file. response info from reporter plugin may NOT be up-to-date. Err: %v", err)
	}

	// register inner reporter plugins
	err = manager.registerInnerReporterPlugins(emitter, metaServer, conf, manager.genericCallback, newReporterPluginInitializers())
	if err != nil {
		return nil, fmt.Errorf("get inner reporter plugin failed: %s", err)
	}

	return manager, nil
}

// newReporterPluginInitializers adds in-tree reporter plugins into init function list
func newReporterPluginInitializers() map[string]plugin.InitFunc {
	innerReporterPluginInitializers := make(map[string]plugin.InitFunc)
	innerReporterPluginInitializers[system.PluginName] = system.NewSystemReporterPlugin
	innerReporterPluginInitializers[kubelet.PluginName] = kubelet.NewKubeletReporterPlugin
	return innerReporterPluginInitializers
}

func (m *ReporterPluginManager) registerInnerReporterPlugins(emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer, conf *config.Configuration, callback plugin.ListAndWatchCallback,
	innerReporterPluginInitializers map[string]plugin.InitFunc,
) error {
	var errList []error

	for pluginName, initFn := range innerReporterPluginInitializers {
		if !general.IsNameEnabled(pluginName, innerReporterPluginsDisabledByDefault, conf.GenericReporterConfiguration.InnerPlugins) {
			klog.Infof("reporter plugin %s is disabled", pluginName)
			continue
		}

		curPlugin, err := initFn(emitter, metaServer, conf, callback)
		if err != nil {
			errList = append(errList, err)
			continue
		}

		err = m.registerPlugin(pluginName, curPlugin)
		if err != nil {
			errList = append(errList, err)
			continue
		}

		m.innerEndpoints.Insert(pluginName)
	}

	if len(errList) > 0 {
		return errors.NewAggregate(errList)
	}

	return nil
}

// GetHandlerType get manage plugin type
func (m *ReporterPluginManager) GetHandlerType() string {
	return registration.ReporterPlugin
}

// ValidatePlugin is to validate the plugin info is supported
func (m *ReporterPluginManager) ValidatePlugin(pluginName string, endpoint string, versions []string) error {
	klog.Infof("[reporter manager] get Plugin %s at Endpoint %s with versions %v", pluginName, endpoint, versions)

	if !m.isVersionCompatibleWithPlugin(versions) {
		return fmt.Errorf("reporter manager version, %s, is not among plugin supported versions %v", pluginapi.Version, versions)
	}

	return nil
}

// RegisterPlugin is to handle plugin register event
func (m *ReporterPluginManager) RegisterPlugin(pluginName, endpoint string, _ []string) error {
	klog.Infof("[reporter manager] registering Plugin %s at Endpoint %s", pluginName, endpoint)

	var cache *v1alpha1.GetReportContentResponse
	// if the plugin is already registered, use the old cache to avoid data loss
	// when the plugin is re-registered.
	m.mutex.Lock()
	old, ok := m.endpoints[pluginName]
	m.mutex.Unlock()
	if ok {
		cache = old.GetCache()
	}

	e, err := plugin.NewRemoteEndpoint(endpoint, pluginName, cache, m.emitter, m.callback)
	if err != nil {
		return fmt.Errorf("failed to dial device plugin with socketPath %s: %v", endpoint, err)
	}

	return m.registerPlugin(pluginName, e)
}

// DeRegisterPlugin is to handler plugin de-register event
func (m *ReporterPluginManager) DeRegisterPlugin(pluginName string) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if e, ok := m.endpoints[pluginName]; ok {
		e.Stop()
		klog.Errorf("[reporter manager] reporter plugin %s has been deregistered", pluginName)
		_ = m.emitter.StoreInt64("reporter_plugin_deregister", 1, metrics.MetricTypeNameCount,
			metrics.ConvertMapToTags(map[string]string{
				"plugin": pluginName,
			})...)
	}
}

// Run start the reporter plugin manager
func (m *ReporterPluginManager) Run(ctx context.Context) {
	go wait.UntilWithContext(ctx, m.syncFunc, m.reconcilePeriod)

	klog.Infof("reporter plugin manager started")
	m.reporter.Run(ctx)
}

func (m *ReporterPluginManager) isVersionCompatibleWithPlugin(versions []string) bool {
	// todo: currently this is fine as we only have a single supported version. When we do need to support
	// 	multiple versions in the future, we may need to extend this function to return a supported version.
	// 	E.g., say kubelet supports v1beta1 and v1beta2, and we get v1alpha1 and v1beta1 from a device plugin,
	// 	this function should return v1beta1
	for _, version := range versions {
		for _, supportedVersion := range v1alpha1.SupportedVersions {
			if version == supportedVersion {
				return true
			}
		}
	}

	return false
}

func (m *ReporterPluginManager) registerPlugin(pluginName string, e plugin.Endpoint) error {
	m.registerEndpoint(pluginName, e)

	success := make(chan bool)

	go m.runEndpoint(pluginName, e, success)

	select {
	case pass := <-success:
		if pass {
			klog.Infof("plugin %s run success", pluginName)
			return nil
		}
		return fmt.Errorf("failed to register plugin %s", pluginName)
	}
}

func (m *ReporterPluginManager) registerEndpoint(pluginName string, e plugin.Endpoint) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	old, ok := m.endpoints[pluginName]

	if ok && !old.IsStopped() {
		klog.Infof("stop old endpoint: %s", pluginName)
		old.Stop()
	}

	m.endpoints[pluginName] = e
	klog.Infof("registered plugin name %s", pluginName)
}

func (m *ReporterPluginManager) runEndpoint(pluginName string, e plugin.Endpoint, success chan<- bool) {
	e.Run(success)
	e.Stop()

	_ = m.emitter.StoreInt64("reporter_plugin_unhealthy", 1, metrics.MetricTypeNameCount,
		metrics.ConvertMapToTags(map[string]string{
			"plugin": pluginName,
		})...)
	klog.Infof("reporter plugin %s became unhealthy", pluginName)
}

// genericCallback is triggered by ListAndWatch of plugin implementations;
// the ListWatch function will store report content in Endpoint and send to manager,
// and the manager can read it from Endpoint cache to obtain content changes initiative
func (m *ReporterPluginManager) genericCallback(pluginName string, _ *v1alpha1.GetReportContentResponse) {
	klog.Infof("genericCallback")
	// get report content from each healthy Endpoint from cache, the last response
	// from this plugin has been already stored to its Endpoint cache before this callback called
	reportResponses, _ := m.getReportContent(true)

	err := m.pushContents(context.Background(), reportResponses)
	if err != nil {
		_ = m.emitter.StoreInt64("reporter_plugin_lw_push_failed", 1, metrics.MetricTypeNameCount, []metrics.MetricTag{
			{Key: "plugin", Val: pluginName},
		}...)
		klog.Errorf("report plugin %s in callback failed with error: %v", pluginName, err)
	}
}

func (m *ReporterPluginManager) pushContents(ctx context.Context, reportResponses map[string]*v1alpha1.GetReportContentResponse) error {
	if err := m.writeCheckpoint(reportResponses); err != nil {
		klog.Errorf("writing checkpoint encountered %v", err)
	}

	return m.reporter.PushContents(ctx, reportResponses)
}

// genericSync periodically calls the Get function to obtain content changes
func (m *ReporterPluginManager) genericSync(ctx context.Context) {
	klog.Infof("genericSync")

	begin := time.Now()
	defer func() {
		costs := time.Since(begin)
		klog.InfoS("finished genericSync", "costs", costs)
		_ = m.emitter.StoreInt64(metricsNameGenericSyncCost, costs.Microseconds(), metrics.MetricTypeNameRaw)
	}()

	// clear unhealthy plugin periodically
	m.clearUnhealthyPlugin()

	// get report content from each healthy Endpoint directly
	reportResponses, _ := m.getReportContent(false)

	pushErr := m.pushContents(ctx, reportResponses)
	if pushErr != nil {
		_ = m.emitter.StoreInt64("reporter_plugin_sync_push_failed", 1, metrics.MetricTypeNameCount)
		klog.Errorf("report plugin failed with error: %v", pushErr)
	}
}

// clearUnhealthyPlugin is to clear stopped plugins from cache which exceeded grace period
func (m *ReporterPluginManager) clearUnhealthyPlugin() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	for pluginName, e := range m.endpoints {
		if e.StopGracePeriodExpired() {
			delete(m.endpoints, pluginName)

			klog.Warningf("plugin %s has been clear", pluginName)
			_ = m.emitter.StoreInt64("reporter_plugin_clear", 1, metrics.MetricTypeNameCount,
				metrics.ConvertMapToTags(map[string]string{
					"plugin": pluginName,
				})...)
		}
	}
}

// getReportContent is to get reportContent from plugins. if cacheFirst is true,
// use plugin cache (when it is no nil), otherwise we call plugin directly.
func (m *ReporterPluginManager) getReportContent(cacheFirst bool) (map[string]*v1alpha1.GetReportContentResponse, error) {
	reportResponses := make(map[string]*v1alpha1.GetReportContentResponse)
	errList := make([]error, 0)

	begin := time.Now()
	m.mutex.Lock()
	defer func() {
		m.mutex.Unlock()
		costs := time.Since(begin)
		klog.InfoS("finished getReportContent cnr", "costs", costs)
		_ = m.emitter.StoreInt64(metricsNameGetContentCost, costs.Microseconds(), metrics.MetricTypeNameRaw)
	}()

	// get report content from each Endpoint
	for pluginName, e := range m.endpoints {
		var (
			resp *v1alpha1.GetReportContentResponse
			err  error
		)

		// if cacheFirst is false or cache response is nil, we will try to get report content directly from plugin
		if cacheFirst {
			cache := e.GetCache()
			if cache != nil {
				reportResponses[pluginName] = cache
				continue
			}
		}

		ctx := metadata.NewOutgoingContext(context.Background(), metadata.New(nil))
		epBegin := time.Now()
		resp, err = e.GetReportContent(ctx)
		epCosts := time.Since(epBegin)
		klog.InfoS("GetReportContent", "costs", epCosts, "pluginName", pluginName)
		_ = m.emitter.StoreInt64(metricsNameGetContentPluginCost, epCosts.Microseconds(), metrics.MetricTypeNameRaw, []metrics.MetricTag{{Key: "plugin", Val: pluginName}}...)
		if err != nil {
			errList = append(errList, err)
			s, _ := status.FromError(err)
			_ = m.emitter.StoreInt64("reporter_plugin_get_content_failed", 1, metrics.MetricTypeNameCount, []metrics.MetricTag{
				{Key: "code", Val: s.Code().String()},
				{Key: "plugin", Val: pluginName},
			}...)

			klog.Errorf("GetReportContentResponse from %s Endpoint failed with error: %v", pluginName, err)
			// if it gets report content failed, uses cached response
			resp = e.GetCache()
		}

		reportResponses[pluginName] = resp
	}

	return reportResponses, errors.NewAggregate(errList)
}

func (m *ReporterPluginManager) writeCheckpoint(reportResponses map[string]*v1alpha1.GetReportContentResponse) error {
	remoteResponses := make(map[string]*v1alpha1.GetReportContentResponse, 0)
	// only write remote endpoint response to checkpoint
	for name, response := range reportResponses {
		if m.innerEndpoints.Has(name) {
			continue
		}
		remoteResponses[name] = response
	}
	data := checkpoint.New(remoteResponses)
	err := m.checkpointManager.CreateCheckpoint(reporterManagerCheckpoint, data)
	if err != nil {
		_ = m.emitter.StoreInt64("reporter_plugin_checkpoint_write_failed", 1, metrics.MetricTypeNameCount)
		return fmt.Errorf("failed to write checkpoint file %q: %v", reporterManagerCheckpoint, err)
	}
	return nil
}

func (m *ReporterPluginManager) readCheckpoint() error {
	reportResponses := make(map[string]*v1alpha1.GetReportContentResponse, 0)
	cp := checkpoint.New(reportResponses)
	err := m.checkpointManager.GetCheckpoint(reporterManagerCheckpoint, cp)
	if err != nil {
		if err == cpmerrors.ErrCheckpointNotFound {
			klog.Warningf("failed to retrieve checkpoint for %q: %v", reporterManagerCheckpoint, err)
			return nil
		}
		return err
	}
	reportResponses = cp.GetData()
	m.mutex.Lock()
	defer m.mutex.Unlock()
	for name, response := range reportResponses {
		// During start up, creates stopped remote endpoint so that the report content
		// will stay zero till the corresponding device plugin re-registers.
		m.endpoints[name] = plugin.NewStoppedRemoteEndpoint(name, response)
	}
	return nil
}
