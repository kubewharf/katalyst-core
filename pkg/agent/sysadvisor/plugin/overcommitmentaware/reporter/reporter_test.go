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

package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/kubewharf/katalyst-api/pkg/consts"

	"github.com/stretchr/testify/assert"

	v1 "k8s.io/api/core/v1"
)

func TestGetReportContent(t *testing.T) {
	p := &OvercommitRatioReporterPlugin{
		manager: NewFakeOvercommitManager(map[v1.ResourceName]float64{}),
	}

	_, err := p.GetReportContent(context.TODO(), nil)
	assert.NotNil(t, err)

	p = &OvercommitRatioReporterPlugin{
		manager: NewFakeOvercommitManager(map[v1.ResourceName]float64{
			v1.ResourceCPU:     1.5123,
			v1.ResourceMemory:  1.2678,
			v1.ResourceStorage: 1.0,
		}),
	}

	res, err := p.GetReportContent(context.TODO(), nil)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(res.Content))

	ratio := map[string]string{}
	err = json.Unmarshal(res.Content[0].Field[0].Value, &ratio)
	assert.NoError(t, err)
	assert.Equal(t, "1.51", ratio[consts.NodeAnnotationCPUOvercommitRatioKey])
	assert.Equal(t, "1.27", ratio[consts.NodeAnnotationMemoryOvercommitRatioKey])
}

func TestStart(t *testing.T) {
	p := &OvercommitRatioReporterPlugin{
		manager: NewFakeOvercommitManager(map[v1.ResourceName]float64{
			v1.ResourceCPU:     1.5123,
			v1.ResourceMemory:  1.2678,
			v1.ResourceStorage: 1.0,
		}),
	}

	assert.Equal(t, overcommitRatioReporterPluginName, p.Name())

	p.Start()
	assert.True(t, p.started)

	p.Stop()
	assert.False(t, p.started)
}

func NewFakeOvercommitManager(data map[v1.ResourceName]float64) OvercommitManager {
	return &FakeOvercommitManager{
		data: data,
	}
}

type FakeOvercommitManager struct {
	data map[v1.ResourceName]float64
}

func (f *FakeOvercommitManager) GetOvercommitRatio() (map[v1.ResourceName]float64, error) {
	if len(f.data) == 0 {
		return nil, fmt.Errorf("empty overcommit ratio data")
	}
	return f.data, nil
}
