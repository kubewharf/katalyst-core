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

package rule

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
)

func makeRuledEvictPod(name, scope string) *RuledEvictPod {
	return &RuledEvictPod{
		EvictPod: &pluginapi.EvictPod{
			Pod: &v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      name,
					Namespace: "default",
					UID:       types.UID(name),
				},
			},
		},
		Scope: scope,
	}
}

func TestEvictionQueue(t *testing.T) {
	for _, tc := range []struct {
		comment string
		q       EvictionQueue

		addResults      [][]string
		popResults      [][]string
		withdrawResults [][]string
	}{
		{
			comment: "with SimpleEvictionQueue",
			q:       NewFIFOEvictionQueue(-1),
			// 1: 1,2,3 + withdraw = false -> 1,2,3
			// 2: 3,4,5 + withdraw = true -> 3,4,5
			// 3: 4,5,6 + withdraw = false -> 3,4,5,6
			// 6: 1,2,3 + withdraw = false -> 1,2,3
			// 7: 3,4,5 + withdraw = false -> 1,2,3,4,5
			addResults: [][]string{
				{"p-1", "p-2", "p-3"},
				{"p-3", "p-4", "p-5"},
				{"p-3", "p-4", "p-5", "p-6"},
				{"p-1", "p-2", "p-3"},
				{"p-1", "p-2", "p-3", "p-4", "p-5"},
			},
			// 4: 3,4,5,6 -> empty
			// 5: empty -> empty
			// 9: 1,3,5 -> empty
			// 10: empty -> empty
			popResults: [][]string{
				{"p-3", "p-4", "p-5", "p-6"},
				{},
				{"p-1", "p-3", "p-5"},
				{},
			},
			// 8: 2,4 -> 1,3,5
			withdrawResults: [][]string{
				{"p-1", "p-3", "p-5"},
			},
		},
		{
			comment: "with FIFOEvictionQueue limit set as 1",
			q:       NewFIFOEvictionQueue(1),
			// 1: 1,2,3 + withdraw = false -> 1,2,3
			// 2: 3,4,5 + withdraw = true -> 3,4,5
			// 3: 4,5,6 + withdraw = false -> 3,4,5,6
			// 6: 1,2,3 + withdraw = false -> 5,6,1,2,3
			// 7: 3,4,5 + withdraw = false -> 5,6,1,2,3,4
			addResults: [][]string{
				{"p-1", "p-2", "p-3"},
				{"p-3", "p-4", "p-5"},
				{"p-3", "p-4", "p-5", "p-6"},
				{"p-5", "p-6", "p-1", "p-2", "p-3"},
				{"p-5", "p-6", "p-1", "p-2", "p-3", "p-4"},
			},
			// 4: 3 -> empty
			// 5: 4 -> empty
			// 9: 5 -> empty
			// 10: 6 -> empty
			popResults: [][]string{
				{"p-3"},
				{"p-4"},
				{"p-5"},
				{"p-6"},
			},
			// 8: 2,4 -> 5,6,1,2
			withdrawResults: [][]string{
				{"p-5", "p-6", "p-1", "p-3"},
			},
		},
		{
			comment: "with FIFOEvictionQueue limit set as 2",
			q:       NewFIFOEvictionQueue(2),
			// 1: 1,2,3 + withdraw = false -> 1,2,3
			// 2: 3,4,5 + withdraw = true -> 3,4,5
			// 3: 4,5,6 + withdraw = false -> 3,4,5,6
			// 6: 1,2,3 + withdraw = false -> 1,2,3
			// 7: 3,4,5 + withdraw = false -> 1,2,3,4,5
			addResults: [][]string{
				{"p-1", "p-2", "p-3"},
				{"p-3", "p-4", "p-5"},
				{"p-3", "p-4", "p-5", "p-6"},
				{"p-1", "p-2", "p-3"},
				{"p-1", "p-2", "p-3", "p-4", "p-5"},
			},
			// 4: 3,4 -> empty
			// 5: 5,6 -> empty
			// 9: 1,3 -> empty
			// 10: 5 -> empty
			popResults: [][]string{
				{"p-3", "p-4"},
				{"p-5", "p-6"},
				{"p-1", "p-3"},
				{"p-5"},
			},
			// 8: 2,4 -> 1,3,5
			withdrawResults: [][]string{
				{"p-1", "p-3", "p-5"},
			},
		},
		{
			comment: "with FIFOEvictionQueue limit set as 2",
			q:       NewFIFOEvictionQueue(3),
			// 1: 1,2,3 + withdraw = false -> 1,2,3
			// 2: 3,4,5 + withdraw = true -> 3,4,5
			// 3: 4,5,6 + withdraw = false -> 3,4,5,6
			// 6: 1,2,3 + withdraw = false -> 1,2,3
			// 7: 3,4,5 + withdraw = false -> 1,2,3,4,5
			addResults: [][]string{
				{"p-1", "p-2", "p-3"},
				{"p-3", "p-4", "p-5"},
				{"p-3", "p-4", "p-5", "p-6"},
				{"p-1", "p-2", "p-3"},
				{"p-1", "p-2", "p-3", "p-4", "p-5"},
			},
			// 4: 3,4,5 -> empty
			// 5: 6 -> empty
			// 9: 1,3,5 -> empty
			// 10: empty -> empty
			popResults: [][]string{
				{"p-3", "p-4", "p-5"},
				{"p-6"},
				{"p-1", "p-3", "p-5"},
				{},
			},
			// 8: 2,4 -> 1,3,5
			withdrawResults: [][]string{
				{"p-1", "p-3", "p-5"},
			},
		},
	} {
		t.Logf("case: %v", tc.comment)
		var rpList RuledEvictPodList

		// 1: 1,2,3 + withdraw = false
		tc.q.Add(
			RuledEvictPodList{
				makeRuledEvictPod("p-1", ""),
				makeRuledEvictPod("p-2", ""),
				makeRuledEvictPod("p-3", ""),
			},
			false,
		)
		assert.Equal(t, tc.addResults[0], tc.q.List().getPodNames())

		// 2: 3,4,5 + withdraw = true
		tc.q.Add(
			RuledEvictPodList{
				makeRuledEvictPod("p-3", ""),
				makeRuledEvictPod("p-4", ""),
				makeRuledEvictPod("p-5", ""),
			},
			true,
		)
		assert.Equal(t, tc.addResults[1], tc.q.List().getPodNames())

		// 3: 4,5,6 + withdraw = false
		tc.q.Add(
			RuledEvictPodList{
				makeRuledEvictPod("p-4", ""),
				makeRuledEvictPod("p-5", ""),
				makeRuledEvictPod("p-6", ""),
			},
			false,
		)
		assert.Equal(t, tc.addResults[2], tc.q.List().getPodNames())

		// 4: pop
		rpList = tc.q.Pop()
		assert.Equal(t, tc.popResults[0], rpList.getPodNames())

		// 5: pop
		rpList = tc.q.Pop()
		assert.Equal(t, tc.popResults[1], rpList.getPodNames())

		// 6: 1,2,3 + withdraw = false
		tc.q.Add(
			RuledEvictPodList{
				makeRuledEvictPod("p-1", ""),
				makeRuledEvictPod("p-2", ""),
				makeRuledEvictPod("p-3", ""),
			},
			false,
		)
		assert.Equal(t, tc.addResults[3], tc.q.List().getPodNames())

		// 7: 3,4,5 + withdraw = false
		tc.q.Add(
			RuledEvictPodList{
				makeRuledEvictPod("p-3", ""),
				makeRuledEvictPod("p-4", ""),
				makeRuledEvictPod("p-5", ""),
			},
			false,
		)
		assert.Equal(t, tc.addResults[4], tc.q.List().getPodNames())

		// 8: 2,4
		tc.q.Withdraw(RuledEvictPodList{
			makeRuledEvictPod("p-2", ""),
			makeRuledEvictPod("p-4", ""),
		})
		assert.Equal(t, tc.withdrawResults[0], tc.q.List().getPodNames())

		// 9: pop
		rpList = tc.q.Pop()
		assert.Equal(t, tc.popResults[2], rpList.getPodNames())

		// 10: pop
		rpList = tc.q.Pop()
		assert.Equal(t, tc.popResults[3], rpList.getPodNames())
	}
}
