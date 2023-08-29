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

package data

import (
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

type CustomMetricLabelKey string

// those const variables define the standard semantics of metric labels
//
// CustomMetricLabelKeyNamespace defines the namespace;
// CustomMetricLabelKeyObject defines the standard kubernetes objects;
// CustomMetricLabelKeyObjectName defines the name of kubernetes objects;
// CustomMetricLabelKeyTimestamp defines the timestamp of this metric;
// CustomMetricLabelSelectorPrefixKey nominates those labels that should be used as selector;
const (
	CustomMetricLabelKeyNamespace      CustomMetricLabelKey = "namespace"
	CustomMetricLabelKeyObject         CustomMetricLabelKey = "object"
	CustomMetricLabelKeyObjectName     CustomMetricLabelKey = "object_name"
	CustomMetricLabelKeyTimestamp      CustomMetricLabelKey = "timestamp"
	CustomMetricLabelSelectorPrefixKey CustomMetricLabelKey = "selector_"
)

// SupportedMetricObject defines those kubernetes objects/CRDs that are supported,
// the mapped values indicate the GVR for the corresponding objects/CRDs
// this cab be set only once
var supportedMetricObject = map[string]schema.GroupVersionResource{
	"nodes": {Version: "v1", Resource: "nodes"},
	"pods":  {Version: "v1", Resource: "pods"},
}

var supportedMetricObjectSettingOnce = sync.Once{}

func AppendSupportedMetricObject(supported map[string]schema.GroupVersionResource) {
	supportedMetricObjectSettingOnce.Do(func() {
		for k, v := range supported {
			supportedMetricObject[k] = v
		}
	})
}

func GetSupportedMetricObject() map[string]schema.GroupVersionResource {
	return supportedMetricObject
}

type MetricData struct {
	Data      int64 `json:"data,omitempty"`
	Timestamp int64 `json:"timestamp,omitempty"`
}

type MetricSeries struct {
	Name   string            `json:"name,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
	Series []*MetricData     `json:"series,omitempty"`
}
