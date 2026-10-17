/*
Copyright 2026 The Katalyst Authors.

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

package dynamicpolicy

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// goroutineFramesContaining dumps every live goroutine stack and counts how many
// mention marker. It is used to detect a goroutine that should have exited but is
// still parked somewhere in the dynamicpolicy package.
func goroutineFramesContaining(t *testing.T, marker string) int {
	t.Helper()
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	return strings.Count(string(buf), marker)
}

// serveForAdvisorServeGoroutineMarker names the anonymous goroutine that runs
// grpcServer.Serve inside serveForAdvisor.
const serveForAdvisorServeGoroutineMarker = "dynamicpolicy.(*DynamicPolicy).serveForAdvisor.func1"

// TestServeForAdvisorStopsWithoutGoroutineLeak pins the exitCh lifecycle. When
// the caller closes stopCh, serveForAdvisor calls grpcServer.Stop() and returns.
// The Serve goroutine then finishes Serve and tries to signal back through
// exitCh. With an UNBUFFERED exitCh that send blocks forever once the main
// goroutine has already left the select, leaking the Serve goroutine. The test
// requires the Serve goroutine frame to be gone shortly after stop, which is
// only possible if exitCh can absorb the send (buffered) or otherwise the
// goroutine has a non-blocking exit.
func TestServeForAdvisorStopsWithoutGoroutineLeak(t *testing.T) {
	t.Parallel()

	// Unix domain sockets have a short sun_path limit (~104 chars); t.TempDir()
	// expands under /var/folders and would exceed it, so use a short /tmp dir.
	dir, err := os.MkdirTemp("/tmp", "ksock")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "cpu-plugin.sock")
	p := &DynamicPolicy{config: configComponent{cpuPluginSocketAbsPath: sock}}
	stopCh := make(chan struct{})

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		p.serveForAdvisor(stopCh)
	}()

	// Wait until the unix socket listener + grpc server are up.
	require.Eventually(t, func() bool {
		_, err := os.Stat(sock)
		return err == nil
	}, 5*time.Second, 20*time.Millisecond, "unix socket did not appear")

	// Let the Serve goroutine settle inside grpcServer.Serve.
	require.Eventually(t, func() bool {
		return goroutineFramesContaining(t, serveForAdvisorServeGoroutineMarker) >= 1
	}, 3*time.Second, 30*time.Millisecond, "Serve goroutine never started")

	close(stopCh)

	select {
	case <-serveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("serveForAdvisor did not return after stopCh was closed")
	}

	// The Serve goroutine must fully exit. On an unbuffered exitCh it parks
	// forever on `exitCh <- struct{}{}` because nobody is left receiving.
	require.Eventually(t, func() bool {
		return goroutineFramesContaining(t, serveForAdvisorServeGoroutineMarker) == 0
	}, 3*time.Second, 50*time.Millisecond,
		"serveForAdvisor Serve goroutine leaked: blocked sending on unbuffered exitCh")
}
