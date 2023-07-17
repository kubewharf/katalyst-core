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

package cpuadvisor

import (
	context "context"
	"testing"

	advisorsvc "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
)

func TestClientAddContainer(t *testing.T) {
	t.Parallel()

	client := NewCPUAdvisorClientStub()
	_, _ = client.AddContainer(context.Background(), &advisorsvc.AddContainerRequest{})
}

func TestClientRemovePod(t *testing.T) {
	t.Parallel()

	client := NewCPUAdvisorClientStub()
	_, _ = client.RemovePod(context.Background(), &advisorsvc.RemovePodRequest{})
}

func TestClientListAndWatch(t *testing.T) {
	t.Parallel()

	client := NewCPUAdvisorClientStub()
	_, _ = client.ListAndWatch(context.Background(), &advisorsvc.Empty{})
}
