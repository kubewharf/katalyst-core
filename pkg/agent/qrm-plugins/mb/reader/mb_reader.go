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
	"fmt"
	"strings"
	"time"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/monitor"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	malachitetypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
)

const (
	tolerationTime = 3 * time.Second
	rootGroup      = "root"
)

type MBData struct {
	MBBody     monitor.GroupMBStats
	UpdateTime int64
}

type MBReader interface {
	// GetMBData yields mb usage rate statistics
	GetMBData() (*MBData, error)
}

// metaServerMBReader converts counter usage into rate usage
type metaServerMBReader struct {
	metricFetcher interface {
		GetByStringIndex(metricName string) interface{}
	}

	currCounterData *malachitetypes.MBData
}

func isDataFresh(epocElapsed int64, now time.Time) bool {
	timestamp := time.Unix(epocElapsed, 0)
	return now.Before(timestamp.Add(tolerationTime))
}

func (m *metaServerMBReader) GetMBData() (*MBData, error) {
	return m.getMBData(time.Now())
}

func (m *metaServerMBReader) getMBData(now time.Time) (*MBData, error) {
	newCounterData, err := m.getCounterData(now)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get mb data from meta server")
	}

	var rate *MBData
	rate, err = calcRateData(newCounterData, m.currCounterData)
	if newCounterData != nil {
		m.currCounterData = newCounterData
	}
	return rate, err
}

// calcRateData derives the rate from two counters
func calcRateData(newCounter, oldCounter *malachitetypes.MBData) (*MBData, error) {
	if newCounter == nil || oldCounter == nil {
		return nil, errors.New("mb rate temporarily unavailable")
	}

	newTimeStamp := time.Unix(newCounter.UpdateTime, 0)
	oldTimeStamp := time.Unix(oldCounter.UpdateTime, 0)
	if !newTimeStamp.After(oldTimeStamp) {
		return nil, errors.New("mb data timestamp no change")
	}
	if newTimeStamp.After(oldTimeStamp.Add(tolerationTime)) {
		return nil, errors.New("mb data too stale to use")
	}

	elapsed := newTimeStamp.Sub(oldTimeStamp)
	stats, err := calcMBRate(newCounter.MBBody, oldCounter.MBBody, elapsed)
	if err != nil {
		return nil, errors.Wrap(err, "failed to calc rate data")
	}

	return &MBData{
		MBBody:     stats,
		UpdateTime: newTimeStamp.Unix(),
	}, nil
}

func calcMBRate(newCounter, oldCounter malachitetypes.MBGroupData, elapsed time.Duration) (monitor.GroupMBStats, error) {
	result := monitor.GroupMBStats{}
	msElapsed := elapsed.Milliseconds()

	// sort out into group's old/new counters
	newGroups, newDataGroups, err := getGroupData(newCounter)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get group data out of new data")
	}
	oldGroups, oldDataGroups, err := getGroupData(oldCounter)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get group data out of old data")
	}

	if !newGroups.Equal(oldGroups) {
		return nil, fmt.Errorf("inconsistent groups %v", newGroups.Difference(oldGroups))
	}

	for group := range newGroups {
		mbInfo, err := calcGroupMBRate(newDataGroups[group], oldDataGroups[group], msElapsed)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to calc mb rate of group %s", group)
		}

		// "root" is alias of "/"
		if rootGroup == group {
			group = "/"
		}
		result[group] = mbInfo
	}

	return result, nil
}

func getGroupData(data malachitetypes.MBGroupData) (sets.String, map[string][]*malachitetypes.MBCCDStat, error) {
	names := sets.String{}
	dataByNames := make(map[string][]*malachitetypes.MBCCDStat)
	for _, ccdData := range data {
		ccdData := ccdData
		groupName := strings.TrimSpace(ccdData.GroupName)
		if len(groupName) == 0 {
			return nil, nil, errors.New("invalid data with empty group name")
		}
		names.Insert(groupName)
		dataByNames[groupName] = append(dataByNames[groupName], &ccdData)
	}
	return names, dataByNames, nil
}

func calcGroupMBRate(newCounter, oldCounter []*malachitetypes.MBCCDStat, msElapsed int64) (monitor.GroupMB, error) {
	result := monitor.GroupMB{}
	oldCounterLookup := map[int]*malachitetypes.MBCCDStat{}
	for _, ccdCounter := range oldCounter {
		oldCounterLookup[ccdCounter.CCDID] = ccdCounter
	}

	for _, ccdCounter := range newCounter {
		ccd := ccdCounter.CCDID
		oldCCDCounter, ok := oldCounterLookup[ccd]
		if !ok {
			return nil, fmt.Errorf("unknown ccd %d", ccd)
		}

		// skip data with overflown, hopefully next round will get valid data
		if ccdCounter.MBLocalCounter < oldCCDCounter.MBLocalCounter ||
			ccdCounter.MBTotalCounter < oldCCDCounter.MBTotalCounter {
			return nil, errors.New("raw counter value started over")
		}

		rateLocalMB := (ccdCounter.MBLocalCounter - oldCCDCounter.MBLocalCounter) * 1000 / msElapsed / 1024 / 1024
		rateTotalMB := (ccdCounter.MBTotalCounter - oldCCDCounter.MBTotalCounter) * 1000 / msElapsed / 1024 / 1024
		result[ccd] = monitor.MBInfo{
			LocalMB:  int(rateLocalMB),
			RemoteMB: int(rateTotalMB - rateLocalMB),
			TotalMB:  int(rateTotalMB),
		}
	}

	return result, nil
}

func (m *metaServerMBReader) getCounterData(now time.Time) (*malachitetypes.MBData, error) {
	data := m.metricFetcher.GetByStringIndex(consts.MetricRealtimeMB)

	if data == nil {
		return nil, fmt.Errorf("got nil by metric key %s", consts.MetricRealtimeMB)
	}

	mbData, ok := data.(*malachitetypes.MBData)
	if !ok {
		return nil, errors.New("invalid data type from metric store")
	}

	if !isDataFresh(mbData.UpdateTime, now) {
		return nil, errors.New("stale mb data in metric store")
	}

	return mbData, nil
}

func New(metricFetcher metrictypes.MetricsFetcher) MBReader {
	return &metaServerMBReader{
		metricFetcher: metricFetcher,
	}
}
