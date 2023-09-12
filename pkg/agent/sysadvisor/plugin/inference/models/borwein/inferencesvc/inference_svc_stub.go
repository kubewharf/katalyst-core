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

package inferencesvc

import (
	context "context"
	fmt "fmt"

	grpc "google.golang.org/grpc"
)

type InferenceServiceStubClient struct {
	fakeResp *InferenceResponse
	wantErr  bool
}

func NewInferenceServiceStubClient() *InferenceServiceStubClient {
	return &InferenceServiceStubClient{}
}

func (isc *InferenceServiceStubClient) Inference(ctx context.Context, in *InferenceRequest, opts ...grpc.CallOption) (*InferenceResponse, error) {
	if isc.wantErr {
		return nil, fmt.Errorf("fake error")
	}

	return isc.fakeResp, nil
}

func (isc *InferenceServiceStubClient) SetFakeResp(fakeResp *InferenceResponse) {
	isc.fakeResp = fakeResp
}

func (isc *InferenceServiceStubClient) RestFakeResp(fakeResp *InferenceResponse) {
	isc.fakeResp = nil
}

func (isc *InferenceServiceStubClient) SetWantError(wantErr bool) {
	isc.wantErr = wantErr
}
