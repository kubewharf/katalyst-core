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
	"sync"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

const (
	// ServiceNamePowerCap also is the unix socket name of the server is listening on
	ServiceNamePowerCap = "node_power_cap"

	metricPowerCappingTargetName  = "power-capping-target"
	metricPowerCappingResetName   = "power-capping-reset"
	metricPowerCappingNoActorName = "power-capping-no-actor"
)

type powerCapService struct {
	sync.Mutex
	capInstruction *capper.CapInstruction
	notify         *fanoutNotifier
	emitter        metrics.MetricEmitter
}

func (p *powerCapService) Init() error {
	return nil
}

func (p *powerCapService) Name() string {
	return ServiceNamePowerCap
}

func (p *powerCapService) AddContainer(ctx context.Context, metadata *advisorsvc.ContainerMetadata) (*advisorsvc.AddContainerResponse, error) {
	return nil, errors.New("not implemented")
}

func (p *powerCapService) RemovePod(ctx context.Context, request *advisorsvc.RemovePodRequest) (*advisorsvc.RemovePodResponse, error) {
	return nil, errors.New("not implemented")
}

func (p *powerCapService) ListAndWatch(empty *advisorsvc.Empty, server advisorsvc.AdvisorService_ListAndWatchServer) error {
	ctx := server.Context()
	ch := p.notify.Register(ctx)

stream:
	for {
		select {
		case <-ctx.Done(): // client disconnected
			klog.Warningf("remote client disconnected")
			break stream
		case <-ch:
			capInst := p.capInstruction
			if capInst == nil {
				break
			}
			resp := capInst.ToListAndWatchResponse()
			err := server.Send(resp)
			if err != nil {
				break stream
			}
		}
	}

	p.notify.Unregister(ctx)
	return nil
}

func (p *powerCapService) Reset() {
	p.emitRawMetric(metricPowerCappingResetName, 1)
	if p.notify.IsEmpty() {
		// todo: log unavailability of down stream component
		klog.Warningf("pap: no power capping plugin connected; Reset op is lost")
		p.emitRawMetric(metricPowerCappingNoActorName, 1)
	}

	p.Lock()
	defer p.Unlock()

	p.capInstruction = capper.PowerCapReset
	p.notify.Notify()
}

func (p *powerCapService) emitRawMetric(name string, value int) {
	if p.emitter == nil {
		return
	}

	_ = p.emitter.StoreInt64(name,
		int64(value),
		metrics.MetricTypeNameRaw,
		metrics.ConvertMapToTags(map[string]string{"pluginName": ServiceNamePowerCap, "pluginType": registration.QoSResourcePlugin})...,
	)
}

func (p *powerCapService) Cap(ctx context.Context, targetWatts, currWatt int) {
	capInst, err := capper.NewCapInstruction(targetWatts, currWatt)
	if err != nil {
		klog.Warningf("invalid cap request: %v", err)
		return
	}

	p.emitRawMetric(metricPowerCappingTargetName, targetWatts)
	if p.notify.IsEmpty() {
		klog.Warningf("pap: no power capping plugin connected; Cap op from %d to %d watt is lost", currWatt, targetWatts)
		p.emitRawMetric(metricPowerCappingNoActorName, 1)
	}

	p.Lock()
	defer p.Unlock()

	p.capInstruction = capInst
	p.notify.Notify()
}

func newPowerCapService() *powerCapService {
	return &powerCapService{
		notify: newNotifier(),
	}
}

func newPowerCapServiceSuite(conf *config.Configuration, emitter metrics.MetricEmitter) (capper.PowerCapper, *grpcServer, error) {
	powerCapSvc := newPowerCapService()
	powerCapSvc.emitter = emitter

	pluginRootFolder := conf.PluginRegistrationDir
	socketPath := path.Join(pluginRootFolder, fmt.Sprintf("%s.sock", powerCapSvc.Name()))

	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, nil, errors.Wrap(err, "failed to clean up the residue file")
	}

	sock, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%v listen %s failed: %v", powerCapSvc.Name(), socketPath, err)
	}

	server := grpc.NewServer()
	advisorsvc.RegisterAdvisorServiceServer(server, powerCapSvc)

	return powerCapSvc, newGRPCServer(server, sock), nil
}

func NewPowerCapPlugin(conf *config.Configuration, emitter metrics.MetricEmitter) (capper.PowerCapper, error) {
	powerCapAdvisor, grpcServer, err := newPowerCapServiceSuite(conf, emitter)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create power capping server")
	}

	grpcServer.Run()
	return powerCapAdvisor, nil
}
