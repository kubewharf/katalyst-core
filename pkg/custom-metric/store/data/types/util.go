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

package types

import (
	"encoding/json"
	"fmt"
	"io"

	"k8s.io/klog/v2"
)

func UnmarshalMetricList(bytes []byte) (res []Metric, err error) {
	var sList []*SeriesMetric
	if err = json.Unmarshal(bytes, &sList); err == nil {
		for _, s := range sList {
			res = append(res, s)
		}
		return
	} else {
		klog.Infof("bytes unmarshalled into series metric err: %v", err)
	}

	var aList []*AggregatedMetric
	if err = json.Unmarshal(bytes, &aList); err == nil {
		for _, a := range aList {
			res = append(res, a)
		}
		return
	} else {
		klog.Infof("bytes unmarshalled into aggregated metric err: %v", err)
	}

	return nil, fmt.Errorf("bytes can be unmarshalled into neither series metric nor aggregated metric")
}

func DecodeMetricList(body io.ReadCloser) (res []Metric, err error) {
	var sList []*SeriesMetric
	if err = json.NewDecoder(body).Decode(&sList); err == nil {
		for _, s := range sList {
			res = append(res, s)
		}
		return
	} else {
		klog.Infof("bytes decoded into series metric err: %v", err)
	}

	var aList []*AggregatedMetric
	if err = json.NewDecoder(body).Decode(&aList); err == nil {
		for _, a := range aList {
			res = append(res, a)
		}
		return
	} else {
		klog.Infof("bytes decoded into aggregated metric err: %v", err)
	}

	return nil, fmt.Errorf("bytes can be decoded into neither series metric nor aggregated metric")
}

func DecodeMetricMetaList(body io.ReadCloser) (res []MetricMeta, err error) {
	var mList []MetricMetaImp
	if err = json.NewDecoder(body).Decode(&mList); err == nil {
		for _, m := range mList {
			res = append(res, m)
		}
		return
	}

	return nil, fmt.Errorf("bytes can't be decoded into metric meta")
}
