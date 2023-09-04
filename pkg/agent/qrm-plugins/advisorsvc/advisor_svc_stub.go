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

package advisorsvc

import (
	"context"

	"google.golang.org/grpc"
)

type stubAdvisorServiceClient struct{}

func (c *stubAdvisorServiceClient) AddContainer(ctx context.Context, in *ContainerMetadata, opts ...grpc.CallOption) (*AddContainerResponse, error) {
	return nil, nil
}

func (c *stubAdvisorServiceClient) RemovePod(ctx context.Context, in *RemovePodRequest, opts ...grpc.CallOption) (*RemovePodResponse, error) {
	return nil, nil
}

func (c *stubAdvisorServiceClient) ListAndWatch(ctx context.Context, in *Empty, opts ...grpc.CallOption) (AdvisorService_ListAndWatchClient, error) {
	return nil, nil
}

func NewStubAdvisorServiceClient() AdvisorServiceClient {
	return &stubAdvisorServiceClient{}
}
