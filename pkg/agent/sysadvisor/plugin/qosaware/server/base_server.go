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
	"reflect"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// Metric names for resource server
const (
	metricServerStartCalled                     = "start_called"
	metricServerStopCalled                      = "stop_called"
	metricServerAddContainerCalled              = "add_container_called"
	metricServerRemovePodCalled                 = "remove_pod_called"
	metricServerStartFailed                     = "start_failed"
	metricServerLWCalled                        = "lw_called"
	metricServerLWGetCheckpointFailed           = "lw_get_checkpoint_failed"
	metricServerLWGetCheckpointSucceeded        = "lw_get_checkpoint_succeeded"
	metricServerGetAdviceCalled                 = "get_advice_called"
	metricServerAdvisorUpdateFailed             = "advisor_update_failed"
	metricServerLWSendResponseFailed            = "lw_send_response_failed"
	metricServerLWSendResponseSucceeded         = "lw_send_response_succeeded"
	metricServerCheckpointUpdateContainerFailed = "checkpoint_update_container_failed"

	healthCheckTolerationDuration = 15 * time.Second
)

type baseServer struct {
	mutex sync.RWMutex

	name              string
	period            time.Duration
	advisorSocketPath string
	pluginSocketPath  string
	stopCh            chan struct{}
	// resourceRequestName and resourceLimitName are field names of types.ContainerInfo
	resourceRequestName string
	resourceLimitName   string

	qosConf *generic.QoSConfiguration

	metaCache  metacache.MetaCache
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter

	grpcServer      *grpc.Server
	resourceServer  subQRMServer
	resourceAdvisor subResourceAdvisor
}

func newBaseServer(
	name string, conf *config.Configuration,
	metaCache metacache.MetaCache, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
	resourceAdvisor subResourceAdvisor,
	resourceServer subQRMServer,
) *baseServer {
	return &baseServer{
		name:            name,
		period:          conf.QoSAwarePluginConfiguration.SyncPeriod,
		qosConf:         conf.QoSConfiguration,
		stopCh:          make(chan struct{}),
		metaCache:       metaCache,
		metaServer:      metaServer,
		emitter:         emitter,
		resourceAdvisor: resourceAdvisor,
		resourceServer:  resourceServer,
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

	go wait.PollImmediateUntil(2*time.Second, func() (bool, error) {
		klog.Infof("[qosaware-server] starting %s", bs.name)
		if err := bs.serve(); err != nil {
			klog.Errorf("[qosaware-server] start %s failed: %q", bs.name, err)
			_ = bs.emitter.StoreInt64(bs.genMetricsName(metricServerStartFailed), 1, metrics.MetricTypeNameRaw)
			return false, nil
		}
		klog.Infof("[qosaware-server] %s exited", bs.name)
		return false, nil
	}, bs.stopCh)

	conn, err := bs.dial(bs.advisorSocketPath, bs.period)
	if err != nil {
		klog.Warningf("[qosaware-server] failed to dial check %s: %q", bs.name, err)
	} else {
		_ = conn.Close()
		klog.Infof("[qosaware-server] %s is ready", bs.name)
	}

	return nil
}

func (bs *baseServer) serve() error {
	advisorSocketDir := path.Dir(bs.advisorSocketPath)

	err := general.EnsureDirectory(advisorSocketDir)
	if err != nil {
		return fmt.Errorf("ensure advisorSocketDir: %s failed with error: %v", advisorSocketDir, err)
	}
	klog.Infof("[qosaware-server] ensure advisorSocketDir: %s successfully", advisorSocketDir)

	if err := os.Remove(bs.advisorSocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %v failed: %v", bs.advisorSocketPath, err)
	}

	klog.Infof("[qosaware-server] %s listen at: %s", bs.name, bs.advisorSocketPath)
	sock, err := net.Listen("unix", bs.advisorSocketPath)
	if err != nil {
		return fmt.Errorf("%v listen %s failed: %v", bs.name, bs.advisorSocketPath, err)
	}
	defer sock.Close()

	klog.Infof("[qosaware-server] %v listen at: %s successfully", bs.name, bs.advisorSocketPath)

	bs.resourceServer.RegisterAdvisorServer()

	klog.Infof("[qosaware-server] starting %s at %v", bs.name, bs.advisorSocketPath)
	if err := bs.grpcServer.Serve(sock); err != nil {
		klog.Errorf("[qosaware-server] %s at %v crashed: %v", bs.name, bs.advisorSocketPath, err)
		return err
	}
	klog.Infof("[qosaware-server] %s exit successfully", bs.name)

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
	bs.mutex.RLock()
	defer bs.mutex.RUnlock()

	close(bs.stopCh)
	_ = bs.emitter.StoreInt64(bs.genMetricsName(metricServerStopCalled), int64(bs.period.Seconds()), metrics.MetricTypeNameCount)

	if bs.grpcServer != nil {
		bs.grpcServer.Stop()
		klog.Infof("[qosaware-server] %v stopped", bs.name)
	}

	return nil
}

func (bs *baseServer) RemovePod(ctx context.Context, request *advisorsvc.RemovePodRequest) (*advisorsvc.RemovePodResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok && sets.NewString(md[util.AdvisorRPCMetadataKeySupportsGetAdvice]...).Has(util.AdvisorRPCMetadataValueSupportsGetAdvice) {
		general.Infof("ignoring RemovePod request from qrm-plugin with GetAdvice support")
		return &advisorsvc.RemovePodResponse{}, nil
	}

	_ = bs.emitter.StoreInt64(bs.genMetricsName(metricServerRemovePodCalled), int64(bs.period.Seconds()), metrics.MetricTypeNameCount)

	if request == nil {
		return nil, fmt.Errorf("remove pod request is nil")
	}

	klog.Infof("[qosaware-server] %v get remove pod request: %v", bs.name, request.PodUid)

	start := time.Now()
	err := bs.metaCache.RemovePod(request.PodUid)
	if err != nil {
		klog.Errorf("[qosaware-server] %v remove pod (%s) with error (time: %s): %v", bs.name, request.PodUid, time.Since(start), err)
	} else {
		klog.Infof("[qosaware-server] %s remove pod (%s) successfully (time: %s)", bs.name, request.PodUid, time.Since(start))
	}

	return &advisorsvc.RemovePodResponse{}, err
}

func (bs *baseServer) AddContainer(ctx context.Context, request *advisorsvc.ContainerMetadata) (*advisorsvc.AddContainerResponse, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if ok && sets.NewString(md[util.AdvisorRPCMetadataKeySupportsGetAdvice]...).Has(util.AdvisorRPCMetadataValueSupportsGetAdvice) {
		general.Infof("ignoring AddContainer request from qrm-plugin with GetAdvice support")
		return &advisorsvc.AddContainerResponse{}, nil
	}

	_ = bs.emitter.StoreInt64(bs.genMetricsName(metricServerAddContainerCalled), int64(bs.period.Seconds()), metrics.MetricTypeNameCount)

	if request == nil {
		klog.Errorf("[qosaware-server] %v get add container request nil", bs.name)
		return nil, fmt.Errorf("add container request nil")
	}
	klog.Infof("[qosaware-server] %v get add container request: %v", bs.name, general.ToString(request))

	start := time.Now()
	err := bs.addContainer(request)
	if err != nil {
		klog.Errorf("[qosaware-server] %v add container (%s/%s) with error (time: %s): %v", bs.name, request.PodUid, request.ContainerName, time.Since(start), err)
	} else {
		klog.Infof("[qosaware-server] %v add container (%s/%s) successfully (time: %s)", bs.name, request.PodUid, request.ContainerName, time.Since(start))
	}

	return &advisorsvc.AddContainerResponse{}, err
}

func (bs *baseServer) addContainer(request *advisorsvc.ContainerMetadata) error {
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

	rv := reflect.ValueOf(containerInfo)
	field := rv.Elem().FieldByName(bs.resourceRequestName)
	if !field.IsValid() {
		return fmt.Errorf("%v is invalid", bs.resourceRequestName)
	}

	if request.UseMilliQuantity {
		field.SetFloat(float64(request.RequestMilliQuantity) / 1000)
	} else {
		field.SetFloat(float64(request.RequestQuantity))
	}

	if err := bs.metaCache.AddContainer(containerInfo.PodUID, containerInfo.ContainerName, containerInfo); err != nil {
		// Try to delete container info in both memory and state file if add container returns error
		_ = bs.metaCache.DeleteContainer(request.PodUid, request.ContainerName)
		return err
	}

	return nil
}
