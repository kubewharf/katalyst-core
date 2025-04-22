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
	"path/filepath"
	"sync"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
	powermetric "github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/metric"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	// ServiceNamePowerCap also is the unix socket name of the server is listening on
	ServiceNamePowerCap = "node_power_cap"

	metricServerGetAdviceCalled = "pap_get_advice_called"

	pollingTimeout = time.Second * 60

	// MetadataApplyPreviousReset is the custom information from power cap client to power cap advisor that
	// allows client to catch up with the reset it just missed by specifying x-apply-previous-reset:yes.
	// Typically, the client should not carry this metadata info anymore with the successive GetAdvice calls.
	MetadataApplyPreviousReset = "x-apply-previous-reset"
)

type longPoller struct {
	timeout       time.Duration
	dataUpdatedCh chan struct{}
}

func (l *longPoller) setDataUpdated() {
	// chan close unblocks all waiting for the ready signal
	close(l.dataUpdatedCh)
	l.dataUpdatedCh = make(chan struct{})
}

type powerCapService struct {
	sync.RWMutex
	started        bool
	capInstruction *capper.CapInstruction
	notify         *fanoutNotifier
	emitter        metrics.MetricEmitter
	grpcServer     *grpcServer

	longPoller *longPoller
}

func (p *powerCapService) hadReset() bool {
	p.RLock()
	defer p.RUnlock()

	// reset op is so critical that it needs to apply even when it was missed
	return p.capInstruction != nil && p.capInstruction.OpCode == capper.OpReset
}

func (p *powerCapService) getCapInstruction() *capper.CapInstruction {
	p.RLock()
	defer p.RUnlock()
	return p.capInstruction
}

func (p *powerCapService) IsCapperReady() bool {
	return !p.notify.IsEmpty()
}

func (p *powerCapService) Stop() error {
	p.Lock()
	defer p.Unlock()
	if !p.started {
		return nil
	}

	p.started = false
	p.grpcServer.server.Stop()
	return nil
}

func (p *powerCapService) Start() error {
	p.Lock()
	defer p.Unlock()

	if p.started {
		return nil
	}

	p.started = true
	p.grpcServer.Run()

	// reset power capping to prevent accumulative effect
	p.requestReset()

	return nil
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

func (p *powerCapService) GetAdvice(ctx context.Context, request *advisorsvc.GetAdviceRequest) (*advisorsvc.GetAdviceResponse, error) {
	return p.getAdviceWithClientReadySignal(ctx, request, nil)
}

// getAdviceWithClientReadySignal has test hook point clientReadyCh, which serves as client signal that it has got hold of
// data-ready channel and server can 'broadcast' the test update
func (p *powerCapService) getAdviceWithClientReadySignal(ctx context.Context, request *advisorsvc.GetAdviceRequest, clientReadyCh chan<- struct{}) (*advisorsvc.GetAdviceResponse, error) {
	_ = p.emitter.StoreInt64(metricServerGetAdviceCalled, 1, metrics.MetricTypeNameCount)
	general.InfofV(6, "pap: get advice request: %v", general.ToString(request))

	if md, ok := metadata.FromIncomingContext(ctx); ok {
		toApplyPreviousReset := md.Get(MetadataApplyPreviousReset)
		if len(toApplyPreviousReset) > 0 && toApplyPreviousReset[0] == "yes" {
			if p.hadReset() {
				return p.getAdvice(ctx, request)
			}
		}
	}

	// when no ready instruction ready yet, fall back to long polling
	serverCtx, cancel := context.WithTimeout(ctx, p.longPoller.timeout)
	defer cancel()

	p.RLock()
	dataUpdatedCh := p.longPoller.dataUpdatedCh
	p.RUnlock()

	// test facility only; production code should always set it nil
	if clientReadyCh != nil {
		clientReadyCh <- struct{}{}
	}

	select {
	case <-dataUpdatedCh:
		return p.getAdvice(ctx, request)
	case <-serverCtx.Done():
		return &advisorsvc.GetAdviceResponse{}, nil
	case <-ctx.Done():
		general.Warningf("pap: get advice aborted by either client disconnection or timeout")
		return nil, errors.New("client disconnected or canceled")
	}
}

func (p *powerCapService) getAdvice(_ context.Context, _ *advisorsvc.GetAdviceRequest) (*advisorsvc.GetAdviceResponse, error) {
	capInst := p.getCapInstruction()
	if capInst == nil {
		return &advisorsvc.GetAdviceResponse{}, nil
	}

	resp := capInst.ToAdviceResponse()
	return resp, nil
}

func (p *powerCapService) deliverPendingReset(ch chan<- struct{}) {
	p.RLock()
	defer p.RUnlock()

	if p.capInstruction != nil && p.capInstruction.OpCode == capper.OpReset {
		ch <- struct{}{}
	}
}

func (p *powerCapService) ListAndWatch(empty *advisorsvc.Empty, server advisorsvc.AdvisorService_ListAndWatchServer) error {
	ctx := wrapFanoutContext(server.Context())
	ch, err := p.notify.Register(ctx)
	if err != nil {
		return errors.Wrap(err, "listAndWatch error")
	}

	// Reset action is critical; ensure it shall be delivered if it was the last one
	p.deliverPendingReset(ch)

stream:
	for {
		select {
		case <-ctx.Done(): // client disconnected
			klog.Warningf("remote client disconnected")
			break stream
		case <-ch:
			capInst := p.getCapInstruction()
			if capInst == nil {
				break
			}
			resp := capInst.ToListAndWatchResponse()
			if err := server.Send(resp); err != nil {
				general.Errorf("pap: [power capping] send response failed: %v", err)
				p.emitErrorCode(powermetric.ErrorCodePowerCapCommunication)
				break stream
			}
		}
	}

	return p.notify.Unregister(ctx)
}

func (p *powerCapService) Reset() {
	if p.notify.IsEmpty() {
		klog.Warningf("pap: no power capping plugin connected; Reset op is lost")
		p.emitErrorCode(powermetric.ErrorCodePowerCapperUnavailable)
	}

	p.Lock()
	defer p.Unlock()

	if !p.started {
		general.Warningf("pap: power capping service is unavailable")
		p.emitErrorCode(powermetric.ErrorCodePowerCapperUnavailable)
		return
	}

	p.emitPowerCapReset()
	p.requestReset()
}

func (p *powerCapService) requestReset() {
	// for LW style server streaming communication
	p.capInstruction = capper.PowerCapReset
	p.notify.Notify()

	// below for unary GetAdvice communication
	p.longPoller.setDataUpdated()
}

func (p *powerCapService) Cap(ctx context.Context, targetWatts, currWatt int) {
	capInst, err := capper.NewCapInstruction(targetWatts, currWatt)
	if err != nil {
		klog.Warningf("invalid cap request: %v", err)
		p.emitErrorCode(powermetric.ErrorCodeOther)
		return
	}

	if p.notify.IsEmpty() {
		klog.Warningf("pap: no power capping plugin connected; Cap op from %d to %d watt is lost", currWatt, targetWatts)
		p.emitErrorCode(powermetric.ErrorCodePowerCapperUnavailable)
	}

	p.Lock()
	defer p.Unlock()

	if !p.started {
		general.Warningf("pap: power capping service is unavailable")
		p.emitErrorCode(powermetric.ErrorCodePowerCapperUnavailable)
		return
	}

	p.emitPowerCapInstruction(capInst)
	p.capInstruction = capInst
	p.notify.Notify()
	p.longPoller.setDataUpdated()
}

func newPowerCapService(emitter metrics.MetricEmitter) *powerCapService {
	return &powerCapService{
		notify:  newNotifier(),
		emitter: emitter,
		longPoller: &longPoller{
			timeout:       pollingTimeout,
			dataUpdatedCh: make(chan struct{}),
		},
	}
}

func newPowerCapServiceSuite(conf *config.Configuration, emitter metrics.MetricEmitter) (*powerCapService, *grpcServer, error) {
	powerCapSvc := newPowerCapService(emitter)

	socketPath := conf.PowerCappingAdvisorSocketAbsPath
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, nil, errors.Wrap(err, "failed to clean up the residue file")
	}

	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return nil, nil, errors.Wrap(err, "failed to create folders to unix sock file")
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

	powerCapAdvisor.grpcServer = grpcServer
	return powerCapAdvisor, nil
}
