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

package server

import (
	"context"
	"fmt"
	"net"
	"os"
	"path"
	"strings"
	"time"

	"google.golang.org/grpc"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// Metric names for resource server
const (
	metricServerStartCalled              = "start_called"
	metricServerStopCalled               = "stop_called"
	metricServerAddContainerCalled       = "add_container_called"
	metricServerRemovePodCalled          = "remove_pod_called"
	metricServerLWCalled                 = "lw_called"
	metricServerLWGetCheckpointFailed    = "lw_get_checkpoint_failed"
	metricServerLWGetCheckpointSucceeded = "lw_get_checkpoint_succeeded"
	metricServerLWSendResponseFailed     = "lw_send_response_failed"
	metricServerLWSendResponseSucceeded  = "lw_send_response_succeeded"
)

type baseServer struct {
	name                string
	period              time.Duration
	advisorSocketPath   string
	pluginSocketPath    string
	recvCh              interface{}
	sendCh              chan struct{}
	lwCalledChan        chan struct{}
	stopCh              chan struct{}
	getCheckpointCalled bool

	metaCache metacache.MetaCache
	emitter   metrics.MetricEmitter

	grpcServer     *grpc.Server
	resourceServer subQRMServer
}

func newBaseServer(name string, conf *config.Configuration, recvCh interface{}, sendCh chan struct{},
	metaCache metacache.MetaCache, emitter metrics.MetricEmitter, resourceServer subQRMServer) *baseServer {
	return &baseServer{
		name:           name,
		period:         conf.QoSAwarePluginConfiguration.SyncPeriod,
		recvCh:         recvCh,
		sendCh:         sendCh,
		lwCalledChan:   make(chan struct{}),
		stopCh:         make(chan struct{}),
		metaCache:      metaCache,
		emitter:        emitter,
		resourceServer: resourceServer,
	}
}

func (bs *baseServer) Name() string {
	return bs.name
}

func (bs *baseServer) genMetricsName(name string) string {
	prefix := strings.ReplaceAll(bs.name, "-", "_")
	name = strings.Join([]string{prefix, name}, "_")
	return name
}

func (bs *baseServer) Start() error {
	_ = bs.emitter.StoreInt64(bs.genMetricsName(metricServerStartCalled), int64(bs.period.Seconds()), metrics.MetricTypeNameCount)

	if err := bs.serve(); err != nil {
		general.Errorf("start %v failed: %v", bs.name, err)
		_ = bs.Stop()
		return err
	}
	general.Infof("%v stared", bs.name)

	go func() {
		for {
			select {
			case <-bs.lwCalledChan:
				return
			case <-bs.stopCh:
				return
			}
		}
	}()

	return nil
}

func (bs *baseServer) serve() error {
	advisorSocketDir := path.Dir(bs.advisorSocketPath)

	err := general.EnsureDirectory(advisorSocketDir)
	if err != nil {
		return fmt.Errorf("ensure advisorSocketDir: %s failed with error: %v", advisorSocketDir, err)
	}

	general.Infof("ensure advisorSocketDir: %s successfully", advisorSocketDir)

	if err := os.Remove(bs.advisorSocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %v failed: %v", bs.advisorSocketPath, err)
	}

	sock, err := net.Listen("unix", bs.advisorSocketPath)
	if err != nil {
		return fmt.Errorf("%v listen %s failed: %v", bs.name, bs.advisorSocketPath, err)
	}

	general.Infof("%v listen at: %s successfully", bs.name, bs.advisorSocketPath)

	bs.resourceServer.RegisterAdvisorServer()

	go func() {
		lastCrashTime := time.Now()
		restartCount := 0
		for {
			general.Infof("%v starting grpc server at %v", bs.name, bs.advisorSocketPath)
			if err := bs.grpcServer.Serve(sock); err == nil {
				break
			}
			general.Errorf("grpc server at %v crashed: %v", bs.advisorSocketPath, err)

			if restartCount > 5 {
				general.Errorf("grpc server at %v has crashed repeatedly recently, quit", bs.advisorSocketPath)
				os.Exit(0)
			}
			timeSinceLastCrash := time.Since(lastCrashTime).Seconds()
			lastCrashTime = time.Now()
			if timeSinceLastCrash > 3600 {
				restartCount = 1
			} else {
				restartCount++
			}
		}
	}()

	if conn, err := bs.dial(bs.advisorSocketPath, bs.period); err != nil {
		return fmt.Errorf("dial check at %v failed: %v", bs.advisorSocketPath, err)
	} else {
		_ = conn.Close()
	}

	return nil
}

// nolint
func (bs *baseServer) dial(unixSocketPath string, timeout time.Duration) (*grpc.ClientConn, error) {
	c, err := grpc.Dial(unixSocketPath, grpc.WithInsecure(), grpc.WithBlock(),
		grpc.WithTimeout(timeout),
		grpc.WithDialer(func(addr string, timeout time.Duration) (net.Conn, error) {
			return net.DialTimeout("unix", addr, timeout)
		}),
	)

	if err != nil {
		return nil, err
	}

	return c, nil
}

func (bs *baseServer) Stop() error {
	close(bs.stopCh)
	_ = bs.emitter.StoreInt64(bs.genMetricsName(metricServerStopCalled), int64(bs.period.Seconds()), metrics.MetricTypeNameCount)

	if bs.grpcServer != nil {
		bs.grpcServer.Stop()
		general.Infof("%v stopped", bs.name)
	}

	if err := os.Remove(bs.advisorSocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %v failed: %v", bs.advisorSocketPath, err)
	}

	return nil
}

func (bs *baseServer) RemovePod(_ context.Context, request *advisorsvc.RemovePodRequest) (*advisorsvc.RemovePodResponse, error) {
	_ = bs.emitter.StoreInt64(bs.genMetricsName(metricServerRemovePodCalled), int64(bs.period.Seconds()), metrics.MetricTypeNameCount)

	if request == nil {
		return nil, fmt.Errorf("remove pod request is nil")
	}
	general.Infof("%v get remove pod request: %v", bs.name, request.PodUid)

	err := bs.metaCache.RemovePod(request.PodUid)
	if err != nil {
		general.Errorf("%v remove pod with error: %v", bs.name, err)
	}

	return &advisorsvc.RemovePodResponse{}, err
}

func (bs *baseServer) AddContainer(_ context.Context, request *advisorsvc.AddContainerRequest) (*advisorsvc.AddContainerResponse, error) {
	_ = bs.emitter.StoreInt64(bs.genMetricsName(metricServerAddContainerCalled), int64(bs.period.Seconds()), metrics.MetricTypeNameCount)

	if request == nil {
		general.Errorf("%v get add container request nil", bs.name)
		return nil, fmt.Errorf("add container request nil")
	}
	general.Infof("%v get add container request: %v", bs.name, general.ToString(request))

	err := bs.addContainer(request)
	if err != nil {
		general.Errorf("%v add container with error: %v", bs.name, err)
	}

	return &advisorsvc.AddContainerResponse{}, err
}

func (bs *baseServer) addContainer(request *advisorsvc.AddContainerRequest) error {
	containerInfo := &types.ContainerInfo{
		PodUID:         request.PodUid,
		PodNamespace:   request.PodNamespace,
		PodName:        request.PodName,
		ContainerName:  request.ContainerName,
		ContainerType:  request.ContainerType,
		ContainerIndex: int(request.ContainerIndex),
		Labels:         request.Labels,
		Annotations:    request.Annotations,
		QoSLevel:       request.QosLevel,
	}

	bs.resourceServer.UpdateContainerResources(request, containerInfo)

	if err := bs.metaCache.AddContainer(containerInfo.PodUID, containerInfo.ContainerName, containerInfo); err != nil {
		// Try to delete container info in both memory and state file if add container returns error
		_ = bs.metaCache.DeleteContainer(request.PodUid, request.ContainerName)
		return err
	}

	return nil
}
