// Copyright 2026 The Katalyst Authors. SPDX-License-Identifier: Apache-2.0
package dra

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/kubewharf/katalyst-api/pkg/plugins/registration"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc"
	v1 "k8s.io/kubelet/pkg/apis/dra/v1"
	beta "k8s.io/kubelet/pkg/apis/dra/v1beta1"
	watcher "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
)

type DRAPlugin interface {
	skeleton.GenericPlugin
	v1.DRAPluginServer
}

// RegisteredPlugin serves kubelet registration and DRA RPCs on a driver-specific
// Unix socket. The lock prevents concurrent instances from sharing an endpoint.
type RegisteredPlugin struct {
	watcher.UnimplementedRegistrationServer
	mu            sync.Mutex
	policy        DRAPlugin
	directory     string
	socket        string
	server        *grpc.Server
	listener      net.Listener
	lock          *os.File
	policyStarted bool
	handler       watcher.RegistrationServer
}

func NewRegisteredPlugin(policy DRAPlugin, directory string) *RegisteredPlugin {
	return &RegisteredPlugin{policy: policy, directory: directory,
		handler: registration.NewRegistrationHandler(watcher.DRAPlugin, policy.Name(), []string{v1.DRAPluginService, beta.DRAPluginService})}
}
func (p *RegisteredPlugin) Name() string { return p.policy.Name() }
func (p *RegisteredPlugin) GetInfo(ctx context.Context, r *watcher.InfoRequest) (*watcher.PluginInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	info, err := p.handler.GetInfo(ctx, r)
	if err != nil {
		return nil, err
	}
	info.Endpoint = p.socket
	return info, nil
}
func (p *RegisteredPlugin) NotifyRegistrationStatus(ctx context.Context, r *watcher.RegistrationStatus) (*watcher.RegistrationStatusResponse, error) {
	return p.handler.NotifyRegistrationStatus(ctx, r)
}
func (p *RegisteredPlugin) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server != nil {
		return nil
	}
	if p.policyStarted {
		return fmt.Errorf("previous DRA policy shutdown is incomplete")
	}
	if err := os.MkdirAll(p.directory, 0755); err != nil {
		return err
	}
	p.socket = filepath.Join(p.directory, p.Name()+".sock")
	if len(p.socket) > 107 {
		return fmt.Errorf("DRA socket path exceeds Unix limit: %s", p.socket)
	}
	lock, err := os.OpenFile(p.socket+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return fmt.Errorf("DRA driver already running: %w", err)
	}
	p.lock = lock
	release := func() { unix.Flock(int(lock.Fd()), unix.LOCK_UN); lock.Close(); p.lock = nil }
	if info, e := os.Lstat(p.socket); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			release()
			return fmt.Errorf("refusing to replace non-socket %s", p.socket)
		}
		conn, dialErr := net.DialTimeout("unix", p.socket, time.Second)
		if dialErr == nil {
			conn.Close()
			release()
			return fmt.Errorf("refusing to replace active DRA socket")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, syscall.ENOENT) {
			release()
			return fmt.Errorf("DRA socket cannot be safely classified as stale: %w", dialErr)
		}
		if err = os.Remove(p.socket); err != nil {
			release()
			return err
		}
	} else if !os.IsNotExist(e) {
		release()
		return e
	}
	if err = p.policy.Start(); err != nil {
		release()
		return err
	}
	p.policyStarted = true
	listener, err := net.Listen("unix", p.socket)
	if err != nil {
		if stopErr := p.policy.Stop(); stopErr == nil {
			p.policyStarted = false
			release()
		}
		return err
	}
	server := grpc.NewServer(grpc.MaxRecvMsgSize(8 << 20))
	watcher.RegisterRegistrationServer(server, p)
	v1.RegisterDRAPluginServer(server, p.policy)
	beta.RegisterDRAPluginServer(server, beta.V1ServerWrapper{DRAPluginServer: p.policy})
	p.server = server
	p.listener = listener
	go server.Serve(listener)
	return nil
}
func (p *RegisteredPlugin) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.server == nil && !p.policyStarted {
		return nil
	}
	if p.server != nil {
		p.server.Stop()
		p.listener.Close()
		p.server = nil
		p.listener = nil
	}
	err := p.policy.Stop()
	if err != nil {
		return err
	}
	p.policyStarted = false
	os.Remove(p.socket)
	if p.lock != nil {
		unix.Flock(int(p.lock.Fd()), unix.LOCK_UN)
		p.lock.Close()
		p.lock = nil
	}
	return err
}
