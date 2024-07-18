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
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSort(t *testing.T) {
	t.Parallel()
	a := time.Now()
	b := a.Add(23 * time.Hour)

	samples := []Sample{
		{
			Value:     0,
			Timestamp: a.Unix(),
		},
		{
			Value:     1,
			Timestamp: b.Unix(),
		},
	}
	sort.Sort(Samples(samples))

	assert.Equal(t, 1.0, samples[0].Value)

	samples = []Sample{
		{
			Value:     0,
			Timestamp: 0,
		},
		{
			Value:     1,
			Timestamp: b.Unix(),
		},
	}
	assert.Equal(t, 0.0, samples[0].Value)
}
