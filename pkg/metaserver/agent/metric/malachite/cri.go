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

package malachite

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
	"google.golang.org/grpc"
	pb "k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
	"k8s.io/kubernetes/pkg/kubelet/util"
)

var defaultRuntimeEndpoint = "unix:///run/containerd/containerd.sock"
var defaultTimeout = 2 * time.Second

type containerInfo struct {
	id   string
	name string
}

func getConnection(endPoint string) (*grpc.ClientConn, error) {
	if endPoint == "" {
		return nil, fmt.Errorf("endpoint is not set")
	}

	var conn *grpc.ClientConn
	addr, dialer, err := util.GetAddressAndDialer(endPoint)
	if err != nil {
		return nil, err
	}
	conn, err = grpc.Dial(addr, grpc.WithInsecure(), grpc.WithBlock(), grpc.WithTimeout(defaultTimeout), grpc.WithContextDialer(dialer))
	if err != nil {
		errMsg := errors.Wrapf(err, "connect endpoint '%s', make sure you are running as root and the endpoint has been started", endPoint)
		return nil, errMsg
	}

	return conn, nil
}

func closeConnection(conn *grpc.ClientConn) error {
	if conn == nil {
		return nil
	}
	return conn.Close()
}

func getRuntimeClientConnection() (*grpc.ClientConn, error) {
	runtimeEndpoint := os.Getenv("CONTAINER_RUNTIME_ENDPOINT")
	if runtimeEndpoint == "" {
		runtimeEndpoint = defaultRuntimeEndpoint
	}
	return getConnection(runtimeEndpoint)
}

func getRuntimeClient() (pb.RuntimeServiceClient, *grpc.ClientConn, error) {
	// Set up a connection to the server.
	conn, err := getRuntimeClientConnection()
	if err != nil {
		return nil, nil, errors.Wrap(err, "connect")
	}
	runtimeClient := pb.NewRuntimeServiceClient(conn)
	return runtimeClient, conn, nil
}

func getPodContainers(runtimeClient pb.RuntimeServiceClient, podID string) ([]*containerInfo, error) {
	filter := &pb.ContainerFilter{
		PodSandboxId: podID,
	}

	request := &pb.ListContainersRequest{
		Filter: filter,
	}

	r, err := runtimeClient.ListContainers(context.Background(), request)
	if err != nil {
		return nil, err
	}

	containersList := r.GetContainers()

	var containers []*containerInfo
	for _, c := range containersList {
		if c.State != pb.ContainerState_CONTAINER_RUNNING {
			continue
		}

		if c.Metadata == nil {
			return nil, fmt.Errorf("pod %s container %s has empty metadata", podID, c.Id)
		}
		containers = append(containers, &containerInfo{
			id:   c.Id,
			name: c.Metadata.Name,
		})
	}

	return containers, nil
}

func getContainerCgroupsPath(client pb.RuntimeServiceClient, containerID string) (string, error) {
	if containerID == "" {
		return "", fmt.Errorf("ID cannot be empty")
	}
	request := &pb.ContainerStatusRequest{
		ContainerId: containerID,
		Verbose:     true,
	}

	r, err := client.ContainerStatus(context.Background(), request)
	if err != nil {
		return "", err
	}

	info := r.GetInfo()
	var cgroupsPath string
	for _, v := range info {
		i := strings.Index(v, "\"cgroupsPath\":")
		if i == -1 {
			continue
		}

		n := i + len("\"cgroupsPath\":")
		str := string(v[n:])

		start := strings.IndexByte(str, '"')
		if start == -1 {
			continue
		}
		str2 := str[start+1:]
		end := strings.IndexByte(str2, '"')
		if end == -1 {
			continue
		}
		cgroupsPath = str2[:end]
		break
	}

	if cgroupsPath == "" {
		return "", fmt.Errorf("failed to find cgroupsPath in container %s inspect info", containerID)
	}

	return cgroupsPath, nil
}

func listPodSandbox(client pb.RuntimeServiceClient) ([]*pb.PodSandbox, error) {
	request := &pb.ListPodSandboxRequest{
		Filter: &pb.PodSandboxFilter{},
	}
	r, err := client.ListPodSandbox(context.Background(), request)
	if err != nil {
		return nil, fmt.Errorf("failed to ListPodSandbox, err %s", err)
	}

	sandboxesList := r.GetItems()
	var readyPods []*pb.PodSandbox
	for _, p := range sandboxesList {
		if p.State == pb.PodSandboxState_SANDBOX_READY {
			readyPods = append(readyPods, p)
		}
	}
	return readyPods, nil
}

func getPodContainersCgroupsPathByPodID(runtimeClient pb.RuntimeServiceClient, podID string) (map[string]string, error) {
	containers, err := getPodContainers(runtimeClient, podID)
	if err != nil {
		return nil, err
	}

	containersCgroupsPath := make(map[string]string)
	for _, ci := range containers {
		cgroupPath, err := getContainerCgroupsPath(runtimeClient, ci.id)
		if err != nil {
			return nil, err
		}
		containersCgroupsPath[ci.name] = cgroupPath
	}

	return containersCgroupsPath, nil
}

func GetAllPodsContainersCgroupsPath() (map[string]map[string]string, error) {
	runtimeClient, runtimeConn, err := getRuntimeClient()
	if err != nil {
		return nil, err
	}
	defer closeConnection(runtimeConn)

	pods, err := listPodSandbox(runtimeClient)
	if err != nil {
		return nil, err
	}

	podsContainersCgroupsPath := make(map[string]map[string]string)
	for _, p := range pods {
		if p.Metadata == nil {
			continue
		}

		if p.Metadata.Uid == "" {
			continue
		}

		podUid := p.Metadata.Uid
		podID := p.Id
		containersCgroupsPath, err := getPodContainersCgroupsPathByPodID(runtimeClient, podID)
		if err != nil {
			return nil, fmt.Errorf("failed to getPodContainersCgroupsPathByPodID, err %v", err)
		}
		podsContainersCgroupsPath[podUid] = containersCgroupsPath

	}
	return podsContainersCgroupsPath, nil
}
