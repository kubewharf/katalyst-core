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

package endpoint

import (
	"context"
	"fmt"
	"sync"
	"time"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const (
	dialRemoteEndpointTimeout = 10 * time.Second
)

const (
	errFailedToDialEvictionPlugin = "failed to dial resource plugin:"

	errEndpointStopped      = "endpoint %v has been stopped"
	endpointStopGracePeriod = 5 * time.Minute
)

// Endpoint represents a single registered plugin. It is responsible
// for managing gRPC communications with the eviction plugin and caching eviction states.
type Endpoint interface {
	ThresholdMet(c context.Context) (*pluginapi.ThresholdMetResponse, error)
	GetTopEvictionPods(c context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error)
	GetEvictPods(c context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error)
	Stop()
	IsStopped() bool
	StopGracePeriodExpired() bool
}

// RemoteEndpointImpl is implement of a remote eviction plugin endpoint
type RemoteEndpointImpl struct {
	client     pluginapi.EvictionPluginClient
	clientConn *grpc.ClientConn

	socketPath string
	pluginName string
	stopTime   time.Time

	mutex sync.Mutex
}

// NewRemoteEndpointImpl new a remote eviction plugin endpoint
func NewRemoteEndpointImpl(socketPath, pluginName string) (*RemoteEndpointImpl, error) {
	c, err := process.Dial(socketPath, dialRemoteEndpointTimeout)
	if err != nil {
		klog.Errorf("[eviction manager] can't create new endpoint with path %s err %v", socketPath, err)
		return nil, fmt.Errorf(errFailedToDialEvictionPlugin+" %v", err)
	}

	return &RemoteEndpointImpl{
		client:     pluginapi.NewEvictionPluginClient(c),
		clientConn: c,

		socketPath: socketPath,
		pluginName: pluginName,
	}, nil
}

// IsStopped check this Endpoint whether be called stop function before
func (e *RemoteEndpointImpl) IsStopped() bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return !e.stopTime.IsZero()
}

// StopGracePeriodExpired check if this Endpoint has been stopped and exceeded the
// grace period since the stop timestamp
func (e *RemoteEndpointImpl) StopGracePeriodExpired() bool {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return !e.stopTime.IsZero() && time.Since(e.stopTime) > endpointStopGracePeriod
}

// SetStopTime is used for testing only
func (e *RemoteEndpointImpl) SetStopTime(t time.Time) {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	e.stopTime = t
}

// ThresholdMet is used to call remote endpoint ThresholdMet
func (e *RemoteEndpointImpl) ThresholdMet(c context.Context) (*pluginapi.ThresholdMetResponse, error) {
	if e.IsStopped() {
		return nil, fmt.Errorf(errEndpointStopped, e)
	}
	ctx, cancel := context.WithTimeout(c, consts.EvictionPluginThresholdMetRPCTimeoutInSecs*time.Second)
	defer cancel()
	return e.client.ThresholdMet(ctx, &pluginapi.Empty{})
}

// GetTopEvictionPods is used to call remote endpoint GetTopEvictionPods
func (e *RemoteEndpointImpl) GetTopEvictionPods(c context.Context, request *pluginapi.GetTopEvictionPodsRequest) (*pluginapi.GetTopEvictionPodsResponse, error) {
	if e.IsStopped() {
		return nil, fmt.Errorf(errEndpointStopped, e)
	}
	ctx, cancel := context.WithTimeout(c, consts.EvictionPluginGetTopEvictionPodsRPCTimeoutInSecs*time.Second)
	defer cancel()
	return e.client.GetTopEvictionPods(ctx, request)
}

// GetEvictPods is used to call remote endpoint GetEvictPods
func (e *RemoteEndpointImpl) GetEvictPods(c context.Context, request *pluginapi.GetEvictPodsRequest) (*pluginapi.GetEvictPodsResponse, error) {
	if e.IsStopped() {
		return nil, fmt.Errorf(errEndpointStopped, e)
	}
	ctx, cancel := context.WithTimeout(c, consts.EvictionPluginGetEvictPodsRPCTimeoutInSecs*time.Second)
	defer cancel()
	return e.client.GetEvictPods(ctx, request)
}

// Stop is used to stop this remote endpoint
func (e *RemoteEndpointImpl) Stop() {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	if e.clientConn != nil {
		e.clientConn.Close()
	}
	e.stopTime = time.Now()
}
