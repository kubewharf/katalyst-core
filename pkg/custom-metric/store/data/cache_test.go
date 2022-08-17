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
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_cache(t *testing.T) {
	c := NewCachedMetric()

	var (
		exist        bool
		names        []string
		oneMetric    []*InternalMetric
		allMetric    []*InternalMetric
		spacedMetric []*InternalMetric
	)

	t.Log("#### 1: Add with none-namespaced metric")

	c.Add(&InternalMetric{
		Name: "m-1",
		Labels: map[string]string{
			"Name": "m-1",
		},
		InternalValue: []*InternalValue{
			{
				Value:     1,
				Timestamp: 1,
			},
		},
	})

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1"}, names)

	oneMetric, exist = c.GetMetric("m-1")
	assert.Equal(t, true, exist)
	assert.Equal(t, &InternalMetric{
		Name: "m-1",
		Labels: map[string]string{
			"Name": "m-1",
		},
		InternalValue: []*InternalValue{
			{
				Value:     1,
				Timestamp: 1,
			},
		},
	}, oneMetric[0])

	oneMetric, exist = c.GetMetric("m-2")
	assert.Equal(t, false, exist)

	t.Log("#### 2: Add with namespaced metric")

	c.Add(&InternalMetric{
		Name:      "m-2",
		Namespace: "n-2",
		Labels: map[string]string{
			"Name": "m-2",
		},
		InternalValue: []*InternalValue{
			{
				Value:     2,
				Timestamp: 3,
			},
		},
	})

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2"}, names)

	oneMetric, exist = c.GetMetric("m-1")
	assert.Equal(t, true, exist)
	assert.Equal(t, &InternalMetric{
		Name: "m-1",
		Labels: map[string]string{
			"Name": "m-1",
		},
		InternalValue: []*InternalValue{
			{
				Value:     1,
				Timestamp: 1,
			},
		},
	}, oneMetric[0])

	oneMetric, exist = c.GetMetric("m-2")
	assert.Equal(t, true, exist)
	assert.Equal(t, &InternalMetric{
		Name:      "m-2",
		Namespace: "n-2",
		Labels: map[string]string{
			"Name": "m-2",
		},
		InternalValue: []*InternalValue{
			{
				Value:     2,
				Timestamp: 3,
			},
		},
	}, oneMetric[0])

	t.Log("#### 3: Add pod with objected metric")

	c.Add(&InternalMetric{
		Name:       "m-3",
		Namespace:  "n-3",
		Object:     "pod",
		ObjectName: "pod-3",
		Labels: map[string]string{
			"Name": "m-3",
		},
		InternalValue: []*InternalValue{
			{
				Value:     4,
				Timestamp: 5,
			},
		},
	})

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2", "m-3"}, names)

	oneMetric, exist = c.GetMetric("m-3")
	assert.Equal(t, true, exist)
	assert.Equal(t, &InternalMetric{
		Name:      "m-3",
		Namespace: "n-3",
		Labels: map[string]string{
			"Name": "m-3",
		},
		Object:     "pod",
		ObjectName: "pod-3",
		InternalValue: []*InternalValue{
			{
				Value:     4,
				Timestamp: 5,
			},
		},
	}, oneMetric[0])

	t.Log("#### 4: Add pod with the same metric Name")

	c.Add(&InternalMetric{
		Name:       "m-3",
		Namespace:  "n-3",
		Object:     "pod",
		ObjectName: "pod-3",
		Labels: map[string]string{
			"Name": "m-3",
		},
		InternalValue: []*InternalValue{
			{
				Value:     7,
				Timestamp: 8,
			},
		},
	})

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2", "m-3"}, names)

	oneMetric, exist = c.GetMetric("m-3")
	assert.Equal(t, true, exist)
	assert.Equal(t, &InternalMetric{
		Name:      "m-3",
		Namespace: "n-3",
		Labels: map[string]string{
			"Name": "m-3",
		},
		Object:     "pod",
		ObjectName: "pod-3",
		InternalValue: []*InternalValue{
			{
				Value:     4,
				Timestamp: 5,
			},
			{
				Value:     7,
				Timestamp: 8,
			},
		},
	}, oneMetric[0])

	t.Log("#### 5: Add pod another meta")

	c.Add(&InternalMetric{
		Name:       "m-3",
		Namespace:  "n-3",
		Object:     "pod",
		ObjectName: "pod-4",
		Labels: map[string]string{
			"Name": "m-3",
		},
		InternalValue: []*InternalValue{
			{
				Value:     10,
				Timestamp: 12,
			},
		},
	})

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2", "m-3"}, names)

	oneMetric, exist = c.GetMetric("m-3")
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*InternalMetric{
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-3",
			InternalValue: []*InternalValue{
				{
					Value:     4,
					Timestamp: 5,
				},
				{
					Value:     7,
					Timestamp: 8,
				},
			},
		},
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-4",
			InternalValue: []*InternalValue{
				{
					Value:     10,
					Timestamp: 12,
				},
			},
		},
	}, oneMetric)

	t.Log("#### 6: Add pod with the duplicated Timestamp")

	c.Add(&InternalMetric{
		Name:       "m-3",
		Namespace:  "n-3",
		Object:     "pod",
		ObjectName: "pod-3",
		Labels: map[string]string{
			"Name":  "m-3",
			"extra": "m-3",
		},
		InternalValue: []*InternalValue{
			{
				Value:     9,
				Timestamp: 8,
			},
			{
				Value:     10,
				Timestamp: 9,
			},
		},
	})

	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-1", "m-2", "m-3"}, names)

	oneMetric, exist = c.GetMetric("m-3")
	assert.Equal(t, true, exist)
	assert.ElementsMatch(t, []*InternalMetric{
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-3",
			InternalValue: []*InternalValue{
				{
					Value:     4,
					Timestamp: 5,
				},
				{
					Value:     7,
					Timestamp: 8,
				},
				{
					Value:     10,
					Timestamp: 9,
				},
			},
		},
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-4",
			InternalValue: []*InternalValue{
				{
					Value:     10,
					Timestamp: 12,
				},
			},
		},
	}, oneMetric)

	t.Log("#### 7: list all metric")

	spacedMetric = c.GetMetricInNamespace("")
	assert.ElementsMatch(t, []*InternalMetric{
		{
			Name: "m-1",
			Labels: map[string]string{
				"Name": "m-1",
			},
			InternalValue: []*InternalValue{
				{
					Value:     1,
					Timestamp: 1,
				},
			},
		},
	}, spacedMetric)

	spacedMetric = c.GetMetricInNamespace("n-2")
	assert.ElementsMatch(t, []*InternalMetric{
		{
			Name:      "m-2",
			Namespace: "n-2",
			Labels: map[string]string{
				"Name": "m-2",
			},
			InternalValue: []*InternalValue{
				{
					Value:     2,
					Timestamp: 3,
				},
			},
		},
	}, spacedMetric)

	spacedMetric = c.GetMetricInNamespace("n-3")
	assert.ElementsMatch(t, []*InternalMetric{
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-3",
			InternalValue: []*InternalValue{
				{
					Value:     4,
					Timestamp: 5,
				},
				{
					Value:     7,
					Timestamp: 8,
				},
				{
					Value:     10,
					Timestamp: 9,
				},
			},
		},
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-4",
			InternalValue: []*InternalValue{
				{
					Value:     10,
					Timestamp: 12,
				},
			},
		},
	}, spacedMetric)

	allMetric = c.ListAllMetric()
	assert.ElementsMatch(t, []*InternalMetric{
		{
			Name: "m-1",
			Labels: map[string]string{
				"Name": "m-1",
			},
			InternalValue: []*InternalValue{
				{
					Value:     1,
					Timestamp: 1,
				},
			},
		},
		{
			Name:      "m-2",
			Namespace: "n-2",
			Labels: map[string]string{
				"Name": "m-2",
			},
			InternalValue: []*InternalValue{
				{
					Value:     2,
					Timestamp: 3,
				},
			},
		},
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-3",
			InternalValue: []*InternalValue{
				{
					Value:     4,
					Timestamp: 5,
				},
				{
					Value:     7,
					Timestamp: 8,
				},
				{
					Value:     10,
					Timestamp: 9,
				},
			},
		},
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-4",
			InternalValue: []*InternalValue{
				{
					Value:     10,
					Timestamp: 12,
				},
			},
		},
	}, allMetric)

	t.Log("#### 8: GC")
	c.gcWithTimestamp(3)
	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-3"}, names)
	allMetric = c.ListAllMetric()
	assert.ElementsMatch(t, []*InternalMetric{
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-3",
			InternalValue: []*InternalValue{
				{
					Value:     4,
					Timestamp: 5,
				},
				{
					Value:     7,
					Timestamp: 8,
				},
				{
					Value:     10,
					Timestamp: 9,
				},
			},
		},
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-4",
			InternalValue: []*InternalValue{
				{
					Value:     10,
					Timestamp: 12,
				},
			},
		},
	}, allMetric)

	c.gcWithTimestamp(8)
	names = c.ListAllMetricNames()
	assert.ElementsMatch(t, []string{"m-3"}, names)
	allMetric = c.ListAllMetric()
	assert.ElementsMatch(t, []*InternalMetric{
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-3",
			InternalValue: []*InternalValue{
				{
					Value:     10,
					Timestamp: 9,
				},
			},
		},
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-4",
			InternalValue: []*InternalValue{
				{
					Value:     10,
					Timestamp: 12,
				},
			},
		},
	}, allMetric)
}

func Test_marshal(t *testing.T) {
	c := NewCachedMetric()

	c.Add(&InternalMetric{
		Name: "m-1",
		Labels: map[string]string{
			"Name": "m-1",
		},
		InternalValue: []*InternalValue{
			{
				Value:     1,
				Timestamp: 1,
			},
		},
	})
	c.Add(&InternalMetric{
		Name:      "m-2",
		Namespace: "n-2",
		Labels: map[string]string{
			"Name": "m-2",
		},
		InternalValue: []*InternalValue{
			{
				Value:     2,
				Timestamp: 3,
			},
		},
	})
	c.Add(&InternalMetric{
		Name:       "m-3",
		Namespace:  "n-3",
		Object:     "pod",
		ObjectName: "pod-3",
		Labels: map[string]string{
			"Name": "m-3",
		},
		InternalValue: []*InternalValue{
			{
				Value:     4,
				Timestamp: 5,
			},
		},
	})
	c.Add(&InternalMetric{
		Name:       "m-3",
		Namespace:  "n-3",
		Object:     "pod",
		ObjectName: "pod-3",
		Labels: map[string]string{
			"Name": "m-3",
		},
		InternalValue: []*InternalValue{
			{
				Value:     7,
				Timestamp: 8,
			},
		},
	})
	c.Add(&InternalMetric{
		Name:       "m-3",
		Namespace:  "n-3",
		Object:     "pod",
		ObjectName: "pod-4",
		Labels: map[string]string{
			"Name": "m-3",
		},
		InternalValue: []*InternalValue{
			{
				Value:     10,
				Timestamp: 12,
			},
		},
	})
	c.Add(&InternalMetric{
		Name:       "m-3",
		Namespace:  "n-3",
		Object:     "pod",
		ObjectName: "pod-3",
		Labels: map[string]string{
			"Name":  "m-3",
			"extra": "m-3",
		},
		InternalValue: []*InternalValue{
			{
				Value:     9,
				Timestamp: 8,
			},
			{
				Value:     10,
				Timestamp: 9,
			},
		},
	})

	target := []*InternalMetric{
		{
			Name: "m-1",
			Labels: map[string]string{
				"Name": "m-1",
			},
			InternalValue: []*InternalValue{
				{
					Value:     1,
					Timestamp: 1,
				},
			},
		},
		{
			Name:      "m-2",
			Namespace: "n-2",
			Labels: map[string]string{
				"Name": "m-2",
			},
			InternalValue: []*InternalValue{
				{
					Value:     2,
					Timestamp: 3,
				},
			},
		},
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name":  "m-3",
				"extra": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-3",
			InternalValue: []*InternalValue{
				{
					Value:     4,
					Timestamp: 5,
				},
				{
					Value:     7,
					Timestamp: 8,
				},
				{
					Value:     10,
					Timestamp: 9,
				},
			},
		},
		{
			Name:      "m-3",
			Namespace: "n-3",
			Labels: map[string]string{
				"Name": "m-3",
			},
			Object:     "pod",
			ObjectName: "pod-4",
			InternalValue: []*InternalValue{
				{
					Value:     10,
					Timestamp: 12,
				},
			},
		},
	}

	allMetric := c.ListAllMetric()
	assert.ElementsMatch(t, target, allMetric)

	bytes, err := Marshal(allMetric)
	assert.Nil(t, err)

	allMetric, err = Unmarshal(bytes)
	assert.Nil(t, err)
	assert.ElementsMatch(t, target, allMetric)

}
