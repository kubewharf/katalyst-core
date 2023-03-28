//go:build linux
// +build linux

// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package external

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
)

var (
	podFetcher = &pod.PodFetcherStub{}
)

func TestInitExternalManager(t *testing.T) {
	externalManager := InitExternalManager(podFetcher)
	assert.NotNil(t, externalManager)

	return
}

func TestSetNetworkManager(t *testing.T) {
	externalManager := InitExternalManager(podFetcher).(*ExternalManagerImpl)
	assert.NotNil(t, externalManager)

	externalManager.start = false

	externalManager.SetNetworkManager(nil)
	assert.Nil(t, externalManager.NetworkManager)
}

func TestRun(t *testing.T) {
	externalManager := InitExternalManager(podFetcher)
	assert.NotNil(t, externalManager)

	ctx, _ := context.WithTimeout(context.TODO(), time.Second)
	externalManager.Run(ctx)
}
