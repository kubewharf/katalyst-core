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

package common

import (
	"fmt"
	"time"
)

type TimeSeries struct {
	Metadata []Metadata

	Samples []Sample
}

type Sample struct {
	Value     float64
	Timestamp int64
}

type Samples []Sample

func (s Samples) Len() int {
	return len(s)
}

func (s Samples) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s Samples) Less(i, j int) bool {
	if s[i].Timestamp == 0 {
		return true
	}
	if s[j].Timestamp == 0 {
		return false
	}

	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.Local
	}
	// sort sample timestamp hour
	houri := time.Unix(s[i].Timestamp, 0).In(location).Hour()
	hourj := time.Unix(s[j].Timestamp, 0).In(location).Hour()

	return houri < hourj
}

type Metadata struct {
	Key   string
	Value string
}

func EmptyTimeSeries() *TimeSeries {
	return &TimeSeries{
		Metadata: []Metadata{},
		Samples:  []Sample{},
	}
}

func (ts *TimeSeries) Add(timeSeries *TimeSeries) error {
	if len(ts.Samples) != len(timeSeries.Samples) {
		return fmt.Errorf("timeSeries points %v and %v not equal", len(ts.Samples), len(timeSeries.Samples))
	}

	for i := range ts.Samples {
		ts.Samples[i].Value += timeSeries.Samples[i].Value
	}
	return nil
}

func (ts *TimeSeries) Max() Sample {
	if len(ts.Samples) == 0 {
		return Sample{
			Value: 0,
		}
	}

	res := ts.Samples[0]
	for _, sample := range ts.Samples {
		if sample.Value > res.Value {
			res = sample
		}
	}
	return res
}

type PredictArgs struct {
	Namespace    string
	WorkloadType string
	WorkloadName string
	Interval     int64 // seconds
	StartTime    int64 // unix timestamp
	Duration     int64 // seconds
	ResourceName string
}
