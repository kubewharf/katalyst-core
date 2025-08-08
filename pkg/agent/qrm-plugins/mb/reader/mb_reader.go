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

package reader

import (
	"time"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	malachitetypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
)

const tolerationTime = 3 * time.Second

type MBReder interface {
	GetMBData() (*malachitetypes.MBData, error)
}

type metaServerMBReader struct {
	metricFetcher interface {
		GetByStringIndex(metricName string) interface{}
	}
}

func isDataFresh(epocElapsed int64, now time.Time) bool {
	timestamp := time.Unix(epocElapsed, 0)
	return now.Before(timestamp.Add(tolerationTime))
}

func (m *metaServerMBReader) GetMBData() (*malachitetypes.MBData, error) {
	return m.getMBData(time.Now())
}

func (m *metaServerMBReader) getMBData(now time.Time) (*malachitetypes.MBData, error) {
	data := m.metricFetcher.GetByStringIndex(consts.MetricRealtimeMB)

	mbData, ok := data.(*malachitetypes.MBData)
	if !ok {
		return nil, errors.New("invalid data type from metric store")
	}

	if !isDataFresh(mbData.UpdateTime, now) {
		return nil, errors.New("stale mb data in metric store")
	}

	return mbData, nil
}

func New() MBReder {
	return &metaServerMBReader{}
}
