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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
)

type fakeServerStartFailed struct {
	name string
}

func (f *fakeServerStartFailed) Name() string {
	return f.name
}

func (f *fakeServerStartFailed) Start() error {
	return fmt.Errorf("start failed")
}

func (f *fakeServerStartFailed) Stop() error {
	return nil
}
func (f *fakeServerStartFailed) RegisterAdvisorServer() {}

type fakeMemoryFailedCount struct {
	*memoryServer
	count int
}

func (f *fakeMemoryFailedCount) Start() error {
	if f.count < 3 {
		f.count++
		return fmt.Errorf("start failed")
	}
	return f.memoryServer.Start()
}

func TestServerStart(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, nil, []*v1.Pod{})
	ms := newTestMemoryServer(t, nil, []*v1.Pod{})

	server := &qrmServerWrapper{serversToRun: map[v1.ResourceName]subQRMServer{
		v1.ResourceCPU:    cs,
		v1.ResourceMemory: ms,
	}}

	ctx, cancel := context.WithCancel(context.Background())
	go server.Run(ctx)
	defer cancel()

	cpuConn, err := cs.dial(cs.advisorSocketPath, cs.period)
	assert.NoError(t, err)
	assert.NotNil(t, cpuConn)
	_ = cpuConn.Close()

	memConn, err := cs.dial(ms.advisorSocketPath, cs.period)
	assert.NoError(t, err)
	assert.NotNil(t, memConn)
	_ = memConn.Close()
}

func TestServerStartWithSomeFailed(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, nil, []*v1.Pod{})
	ms := newTestMemoryServer(t, nil, []*v1.Pod{})

	server := &qrmServerWrapper{serversToRun: map[v1.ResourceName]subQRMServer{
		"a": &fakeServerStartFailed{name: "a"},
		"b": &fakeServerStartFailed{name: "b"},
		"c": &fakeServerStartFailed{name: "c"},
		"d": &fakeServerStartFailed{name: "d"},
		"e": &fakeServerStartFailed{name: "e"},
		"f": &fakeServerStartFailed{name: "f"},
		"g": &fakeServerStartFailed{name: "g"},

		v1.ResourceCPU:    cs,
		v1.ResourceMemory: ms,
	}}

	ctx, cancel := context.WithCancel(context.Background())
	go server.Run(ctx)
	defer cancel()

	cpuConn, err := cs.dial(cs.advisorSocketPath, cs.period)
	assert.NoError(t, err)
	assert.NotNil(t, cpuConn)
	_ = cpuConn.Close()

	memConn, err := cs.dial(ms.advisorSocketPath, cs.period)
	assert.NoError(t, err)
	assert.NotNil(t, memConn)
	_ = memConn.Close()
}

func TestServerStartRecovery(t *testing.T) {
	t.Parallel()

	cs := newTestCPUServer(t, nil, []*v1.Pod{})
	ms := newTestMemoryServer(t, nil, []*v1.Pod{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	server := &qrmServerWrapper{serversToRun: map[v1.ResourceName]subQRMServer{
		"a": &fakeServerStartFailed{name: "a"},
		"b": &fakeServerStartFailed{name: "b"},
		"c": &fakeServerStartFailed{name: "c"},
		"d": &fakeServerStartFailed{name: "d"},
		"e": &fakeServerStartFailed{name: "e"},
		"f": &fakeServerStartFailed{name: "f"},
		"g": &fakeServerStartFailed{name: "g"},

		v1.ResourceCPU:    cs,
		v1.ResourceMemory: &fakeMemoryFailedCount{memoryServer: ms},
	}}

	go server.Run(ctx)

	cpuConn, err := cs.dial(cs.advisorSocketPath, 30*time.Second)
	assert.NoError(t, err)
	assert.NotNil(t, cpuConn)
	_ = cpuConn.Close()

	memConn, err := cs.dial(ms.advisorSocketPath, 30*time.Second)
	assert.NoError(t, err)
	assert.NotNil(t, memConn)
	_ = memConn.Close()
}
