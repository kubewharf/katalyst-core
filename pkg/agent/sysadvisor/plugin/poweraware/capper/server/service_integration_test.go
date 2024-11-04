//go:build integration
// +build integration

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
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/capper"
)

func testClient(lis *bufconn.Listener, t *testing.T) {
	// set up test client
	bufDialer := func(context.Context, string) (net.Conn, error) {
		return lis.Dial()
	}

	ctx := context.TODO()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(bufDialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("failed to set up buffconn for test: %v", err)
	}
	defer conn.Close()

	client := advisorsvc.NewAdvisorServiceClient(conn)
	stream, err := client.ListAndWatch(ctx, &advisorsvc.Empty{})
	if err != nil {
		t.Fatalf("test client failed to connect to test server: %v", err)
	}

	// only receive 3 messages out of stream, for test purpose
	for i := 0; i < 3; i++ {
		lwResp, err := stream.Recv()
		if err != nil {
			t.Logf("test client failed to get next message in stream: %v", err)
			break
		}
		capInsts, err := capper.GetCappingInstructions(lwResp)
		for _, ci := range capInsts {
			t.Logf("recv: %#v", ci)
		}
	}

	// to simulate client disconnect
	conn.Close()
}

// integration test takes lengthy time to finish; NOT to run as normal unit test
func Test_powerCapAdvisorPluginServer_Cap_Client_Recv(t *testing.T) {
	t.Parallel()

	lis := bufconn.Listen(101024 * 1024)
	svc := newPowerCapService()
	baseServer := grpc.NewServer()
	advisorsvc.RegisterAdvisorServiceServer(baseServer, svc)
	svc.started = true
	go func() {
		if err := baseServer.Serve(lis); err != nil {
			fmt.Printf("error serving server: %v/n", err)
		}
	}()

	// start 1st test client in background
	go func() {
		testClient(lis, t)
	}()

	// elapse a bit to let client has established connection via LW, then
	time.Sleep(time.Second * 1)
	// test server to send out capping requests
	svc.Cap(context.TODO(), 80, 100)
	time.Sleep(time.Second * 1)
	// svc.Cap(context.TODO(), 80, 98)
	svc.Reset() // this reset will be delivered to the future-client even though it is not yet connected
	// expecting client-1 receive such capping req
	time.Sleep(time.Second * 1)

	// start 2nd test client at background
	go func() {
		testClient(lis, t)
	}()

	// wait a while for test-2 LW
	time.Sleep(time.Second * 1)
	svc.Cap(context.TODO(), 80, 95)
	// expecting test-1 + test-2 both receive above capping req

	// continue with one more
	time.Sleep(time.Second * 1)
	svc.Cap(context.TODO(), 80, 90)
	// expecting test-1, test-2 both receive

	// test-1 has already received 3 req; hence it is going to disconnect
	// expecting server able to detect such disconnection

	// continue with more reqs
	time.Sleep(time.Second * 1)
	svc.Cap(context.TODO(), 80, 85)
	// expecting test-2 receive it

	// and test-2 disconnect itself, after having received 3 req

	// server is still able to attempt to send out more reqs, whether there is alive client or not
	time.Sleep(time.Second * 1)
	svc.Cap(context.TODO(), 78, 84)
	time.Sleep(time.Second * 1)
	svc.Cap(context.TODO(), 78, 83)
	time.Sleep(time.Second * 1)
	svc.Cap(context.TODO(), 78, 82)

	time.Sleep(time.Second * 5)
}
