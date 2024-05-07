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

package plugin

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	dialRemoteEndpointTimeout = 10 * time.Second
)

// ListAndWatchCallback should be called when plugins report info update.
type ListAndWatchCallback func(string, *v1alpha1.GetReportContentResponse)

// Endpoint represents a single registered plugin. It is responsible
// for managing gRPC communications with the reporter plugin and caching reporter states.
type Endpoint interface {
	// Run initializes a ListAndWatch steam which will send a signal to the success channel
	// when it initializes successfully
	Run(success chan<- bool)
	// Stop will be call when this Endpoint was de-registered or some error happened in ListAndWatch
	Stop()
	// GetReportContent will call rpc GetReportContent to plugin directly
	GetReportContent(c context.Context) (*v1alpha1.GetReportContentResponse, error)
	// ListAndWatchReportContentCallback will be call when this Endpoint receives plugin ListAndWatch send
	ListAndWatchReportContentCallback(string, *v1alpha1.GetReportContentResponse)
	// GetCache get response cache of this Endpoint
	GetCache() *v1alpha1.GetReportContentResponse
	// IsStopped check this Endpoint whether be called stop function before
	IsStopped() bool
	// StopGracePeriodExpired check if this Endpoint has been stopped and exceeded the
	// grace period since the stop timestamp
	StopGracePeriodExpired() bool
}

// NewRemoteEndpoint creates a new Endpoint for the given reporter' plugin name.
// This is to be used during normal reporter' plugin registration.
func NewRemoteEndpoint(socketPath, pluginName string, cache *v1alpha1.GetReportContentResponse,
	emitter metrics.MetricEmitter, callback ListAndWatchCallback,
) (Endpoint, error) {
	c, err := process.Dial(socketPath, dialRemoteEndpointTimeout)
	if err != nil {
		klog.Errorf("Can't create new Endpoint with path %s err %v", socketPath, err)
		return nil, err
	}

	return &remoteEndpointImpl{
		client:     v1alpha1.NewReporterPluginClient(c),
		clientConn: c,

		socketPath: socketPath,
		pluginName: pluginName,
		cache:      cache,
		emitter:    emitter,

		cb:          callback,
		StopControl: process.NewStopControl(time.Time{}),
	}, nil
}

// NewStoppedRemoteEndpoint creates a new Endpoint for the given pluginName with stopTime set.
// This is to be used during Agent restart, before the actual reporter plugin re-registers.
func NewStoppedRemoteEndpoint(pluginName string, cache *v1alpha1.GetReportContentResponse) Endpoint {
	return &remoteEndpointImpl{
		pluginName:  pluginName,
		cache:       cache,
		StopControl: process.NewStopControl(time.Now()),
	}
}

type remoteEndpointImpl struct {
	client     v1alpha1.ReporterPluginClient
	clientConn *grpc.ClientConn

	cache      *v1alpha1.GetReportContentResponse
	socketPath string
	pluginName string
	emitter    metrics.MetricEmitter

	cb ListAndWatchCallback

	mutex sync.Mutex
	*process.StopControl
}

// Run initializes ListAndWatch gRPC call for the plugin and blocks
// on receiving ListAndWatch gRPC stream updates. Each stream-item
// for ListAndWatch contains a new list of report content.
// It then triggers the callback function to pass this item to the manager.
func (e *remoteEndpointImpl) Run(success chan<- bool) {
	stream, err := e.client.ListAndWatchReportContent(context.Background(), &v1alpha1.Empty{})
	if err != nil {
		s, _ := status.FromError(err)
		_ = e.emitter.StoreInt64("reporter_plugin_lw_content_failed", 1, metrics.MetricTypeNameCount, []metrics.MetricTag{
			{Key: "code", Val: s.Code().String()},
			{Key: "plugin", Val: e.pluginName},
		}...)
		klog.Errorf("ListAndWatch ended unexpectedly for reporter plugin %s with error %v", e.pluginName, err)
		success <- false
		return
	}

	success <- true

	for {
		response, err := stream.Recv()
		if err != nil {
			s, _ := status.FromError(err)
			_ = e.emitter.StoreInt64("reporter_plugin_lw_recv_failed", 1, metrics.MetricTypeNameCount, []metrics.MetricTag{
				{Key: "code", Val: s.Code().String()},
				{Key: "plugin", Val: e.pluginName},
			}...)
			klog.Errorf("ListAndWatch recv failed for reporter plugin %s with error %v", e.pluginName, err)
			err := stream.CloseSend()
			if err != nil {
				s, _ := status.FromError(err)
				_ = e.emitter.StoreInt64("reporter_plugin_lw_close_failed", 1, metrics.MetricTypeNameCount, []metrics.MetricTag{
					{Key: "code", Val: s.Code().String()},
					{Key: "plugin", Val: e.pluginName},
				}...)
				klog.Errorf("ListAndWatch close send failed for reporter plugin %s with error %v", e.pluginName, err)
			}
			return
		}

		klog.V(2).Infof("content list pushed for reporter plugin %s", e.pluginName)

		e.ListAndWatchReportContentCallback(e.pluginName, response)
	}
}

// Stop close client connection and set stop timestamp
func (e *remoteEndpointImpl) Stop() {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	if e.clientConn != nil {
		_ = e.clientConn.Close()
	}

	e.StopControl.Stop()
}

// GetReportContent get report content by rpc call directly and store to cache if it is successful
func (e *remoteEndpointImpl) GetReportContent(c context.Context) (*v1alpha1.GetReportContentResponse, error) {
	if e.IsStopped() {
		return nil, fmt.Errorf("endpoint %v has been stopped", e.pluginName)
	}

	resp, err := e.client.GetReportContent(c, &v1alpha1.Empty{})
	if err == nil {
		e.setCache(resp)
	}

	return resp, err
}

// ListAndWatchReportContentCallback store to cache first and then call callback function
func (e *remoteEndpointImpl) ListAndWatchReportContentCallback(pluginName string, response *v1alpha1.GetReportContentResponse) {
	e.setCache(response)

	e.cb(pluginName, response)
}

func (e *remoteEndpointImpl) GetCache() *v1alpha1.GetReportContentResponse {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	return e.cache
}

func (e *remoteEndpointImpl) setCache(cache *v1alpha1.GetReportContentResponse) {
	e.mutex.Lock()
	defer e.mutex.Unlock()

	e.cache = cache
}
