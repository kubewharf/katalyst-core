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

package metrics

import "context"

// MetricTagWrapper is a wrapped implementation for MetricEmitter
// it contains a standard MetricEmitter implementation along with
// pre-defined common metrics tags
type MetricTagWrapper struct {
	// unitTag is a special tag to indicate its owner component unit
	// commonTags are those tags that should be added by default
	unitTag    MetricTag
	commonTags []MetricTag

	MetricEmitter
}

var _ MetricEmitter = &MetricTagWrapper{}

func (t *MetricTagWrapper) StoreInt64(key string, val int64, emitType MetricTypeName, tags ...MetricTag) error {
	tags = append(tags, t.commonTags...)
	tags = append(tags, t.unitTag)
	return t.MetricEmitter.StoreInt64(key, val, emitType, tags...)
}

func (t *MetricTagWrapper) StoreFloat64(key string, val float64, emitType MetricTypeName, tags ...MetricTag) error {
	tags = append(tags, t.commonTags...)
	tags = append(tags, t.unitTag)
	return t.MetricEmitter.StoreFloat64(key, val, emitType, tags...)
}

func (t *MetricTagWrapper) Run(_ context.Context) {}

func (t *MetricTagWrapper) WithTags(unit string, commonTags ...MetricTag) MetricEmitter {
	return t.deepCopy().withTags(unit, commonTags...)
}

func (t *MetricTagWrapper) withTags(unit string, commonTags ...MetricTag) MetricEmitter {
	t.unitTag = MetricTag{
		Key: "emmit_unit",
		Val: unit,
	}

	t.addOrUpdateCommonTags(commonTags)
	return t
}

func (t *MetricTagWrapper) deepCopy() *MetricTagWrapper {
	newMetricTagWrapper := &MetricTagWrapper{MetricEmitter: t.MetricEmitter}
	newMetricTagWrapper.unitTag = t.unitTag
	newMetricTagWrapper.commonTags = append(newMetricTagWrapper.commonTags, t.commonTags...)
	return newMetricTagWrapper
}

// addOrUpdateCommonTags tries to add a tag to common tags list.
func (t *MetricTagWrapper) addOrUpdateCommonTags(tags []MetricTag) {
	for _, tag := range tags {
		exist := false
		for i := range t.commonTags {
			if tag.Key == t.commonTags[i].Key {
				t.commonTags[i].Val = tag.Val
				exist = true
				break
			}
		}
		if !exist {
			t.commonTags = append(t.commonTags, tag)
		}
	}
}
