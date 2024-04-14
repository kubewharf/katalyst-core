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

package options

import (
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestWorkloadProfilingOptions(t *testing.T) {
	t.Parallel()

	workload := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pod",
			Namespace: "ns",
			Annotations: map[string]string{
				"aa": "bb",
				"cc": "dd",
				"ee": "",
			},
			Labels: map[string]string{
				"11": "22",
				"33": "44",
				"55": "",
			},
		},
	}

	tests := []struct {
		name    string
		options WorkloadProfilingOptions
		matched bool
	}{
		{
			name:    "1",
			options: WorkloadProfilingOptions{},
			matched: false,
		},
		{
			name: "2",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AntiNamespaces:   []string{"ns"},
			},
			matched: false,
		},
		{
			name: "3",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				Namespaces:       []string{"ns"},
				AnnoSelector:     "aa=bb",
				LabelSelector:    "11=22",
			},
			matched: true,
		},
		{
			name: "4",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				Namespaces:       []string{"ns"},
				AnnoSelector:     "aa!=bb",
				LabelSelector:    "11=22",
			},
			matched: false,
		},
		{
			name: "5",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				Namespaces:       []string{"ns"},
				AnnoSelector:     "ff!=gg",
				LabelSelector:    "11=22",
			},
			matched: true,
		},
		{
			name: "6",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,cc=dd",
				LabelSelector:    "11=22,33=44",
			},
			matched: true,
		},
		{
			name: "7",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,cc=ff",
				LabelSelector:    "11=22",
			},
			matched: false,
		},
		{
			name: "8",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,ee",
				LabelSelector:    "11=22,55",
			},
			matched: true,
		},
		{
			name: "9",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,ff",
				LabelSelector:    "11=22,55",
			},
			matched: false,
		},
		{
			name: "10",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa",
				LabelSelector:    "11=22,55",
			},
			matched: true,
		},
		{
			name: "11",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,ee=",
				LabelSelector:    "11=22,55",
			},
			matched: true,
		},
		{
			name: "12",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,ee,ff!=",
				LabelSelector:    "11=22,55",
			},
			matched: true,
		},
		{
			name: "13",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,ee!=,ff!=",
				LabelSelector:    "11=22,55",
			},
			matched: false,
		},
		{
			name: "14",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,ee!=",
				LabelSelector:    "11=22",
			},
			matched: false,
		},
		{
			name: "15",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,!cc",
				LabelSelector:    "11=22",
			},
			matched: false,
		},
		{
			name: "15",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,!ee",
				LabelSelector:    "11=22",
			},
			matched: false,
		},
		{
			name: "15",
			options: WorkloadProfilingOptions{
				ExplicitChecking: true,
				AnnoSelector:     "aa=bb,!ff",
				LabelSelector:    "11=22",
			},
			matched: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f, ok, err := tt.options.getWorkloadEnableFunc()
			assert.NoError(t, err)
			if ok {
				assert.Equal(t, tt.matched, f(workload))
			}
		})
	}
}
