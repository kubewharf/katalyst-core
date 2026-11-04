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

package podkiller

import (
	"context"
	"fmt"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type recordingRuleKiller struct {
	name    string
	evicted bool
}

func (r *recordingRuleKiller) Evict(context.Context, *v1.Pod, int64, string, string) error {
	r.evicted = true
	return nil
}

func (r *recordingRuleKiller) Name() string { return r.name }

type testKillerRule struct {
	name     string
	priority int
	matched  bool
	killer   Killer
}

func (r testKillerRule) Name() string  { return r.name }
func (r testKillerRule) Priority() int { return r.priority }
func (r testKillerRule) Match(*v1.Pod) (bool, Killer) {
	return r.matched, r.killer
}

func TestRuleBasedKillerUsesHighestPriorityMatchingRule(t *testing.T) {
	t.Parallel()

	defaultKiller := &recordingRuleKiller{name: "default"}
	lowKiller := &recordingRuleKiller{name: "low"}
	highKiller := &recordingRuleKiller{name: "high"}

	killer, err := NewRuleBasedKiller(defaultKiller, []KillerRule{
		testKillerRule{name: "low", priority: 100, matched: true, killer: lowKiller},
		testKillerRule{name: "high", priority: 1000, matched: true, killer: highKiller},
	})
	if err != nil {
		t.Fatalf("NewRuleBasedKiller failed: %v", err)
	}

	if err := killer.Evict(context.Background(), &v1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod"}}, 0, "reason", "plugin"); err != nil {
		t.Fatalf("Evict failed: %v", err)
	}
	if !highKiller.evicted {
		t.Fatalf("expected high priority killer to evict")
	}
	if lowKiller.evicted || defaultKiller.evicted {
		t.Fatalf("expected lower priority and default killers not to evict")
	}
}

func TestRuleBasedKillerFallsBackToDefaultWhenNoRuleMatches(t *testing.T) {
	t.Parallel()

	defaultKiller := &recordingRuleKiller{name: "default"}
	ruleKiller := &recordingRuleKiller{name: "rule"}

	killer, err := NewRuleBasedKiller(defaultKiller, []KillerRule{
		testKillerRule{name: "rule", priority: 100, matched: false, killer: ruleKiller},
	})
	if err != nil {
		t.Fatalf("NewRuleBasedKiller failed: %v", err)
	}

	if err := killer.Evict(context.Background(), &v1.Pod{}, 0, "reason", "plugin"); err != nil {
		t.Fatalf("Evict failed: %v", err)
	}
	if !defaultKiller.evicted {
		t.Fatalf("expected default killer to evict")
	}
	if ruleKiller.evicted {
		t.Fatalf("expected unmatched rule killer not to evict")
	}
}

func TestRuleBasedKillerSortsSamePriorityByName(t *testing.T) {
	t.Parallel()

	aKiller := &recordingRuleKiller{name: "a"}
	bKiller := &recordingRuleKiller{name: "b"}

	killer, err := NewRuleBasedKiller(&recordingRuleKiller{name: "default"}, []KillerRule{
		testKillerRule{name: "b-rule", priority: 100, matched: true, killer: bKiller},
		testKillerRule{name: "a-rule", priority: 100, matched: true, killer: aKiller},
	})
	if err != nil {
		t.Fatalf("NewRuleBasedKiller failed: %v", err)
	}

	if err := killer.Evict(context.Background(), &v1.Pod{}, 0, "reason", "plugin"); err != nil {
		t.Fatalf("Evict failed: %v", err)
	}
	if !aKiller.evicted || bKiller.evicted {
		t.Fatalf("expected lexicographically first rule to win")
	}
}

func TestNewRuleBasedKillerRejectsNilDefaultKiller(t *testing.T) {
	t.Parallel()

	_, err := NewRuleBasedKiller(nil, nil)
	if err == nil {
		t.Fatalf("expected nil default killer error")
	}
}

func TestNewRuleBasedKillerRejectsNilRule(t *testing.T) {
	t.Parallel()

	_, err := NewRuleBasedKiller(&recordingRuleKiller{name: "default"}, []KillerRule{nil})
	if err == nil {
		t.Fatalf("expected nil rule error")
	}
}

func TestRuleBasedKillerReturnsErrorWhenMatchedRuleHasNilKiller(t *testing.T) {
	t.Parallel()

	killer, err := NewRuleBasedKiller(&recordingRuleKiller{name: "default"}, []KillerRule{
		testKillerRule{name: "bad-rule", priority: 100, matched: true, killer: nil},
	})
	if err != nil {
		t.Fatalf("NewRuleBasedKiller failed: %v", err)
	}

	err = killer.Evict(context.Background(), &v1.Pod{}, 0, "reason", "plugin")
	if err == nil || fmt.Sprint(err) == "" {
		t.Fatalf("expected matched nil killer error")
	}
}
