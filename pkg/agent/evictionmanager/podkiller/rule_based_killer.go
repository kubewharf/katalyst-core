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
	"sort"

	v1 "k8s.io/api/core/v1"
)

const KillerNameRuleBasedKiller = "rule-based-killer"

type KillerRule interface {
	Name() string
	Priority() int
	Match(pod *v1.Pod) (matched bool, killer Killer)
}

type ruleBasedKiller struct {
	defaultKiller Killer
	rules         []KillerRule
}

func NewRuleBasedKiller(defaultKiller Killer, rules []KillerRule) (Killer, error) {
	if defaultKiller == nil {
		return nil, fmt.Errorf("default killer must not be nil")
	}
	for i, rule := range rules {
		if rule == nil {
			return nil, fmt.Errorf("killer rule %d must not be nil", i)
		}
	}

	sortedRules := append([]KillerRule(nil), rules...)
	sort.SliceStable(sortedRules, func(i, j int) bool {
		if sortedRules[i].Priority() != sortedRules[j].Priority() {
			return sortedRules[i].Priority() > sortedRules[j].Priority()
		}
		return sortedRules[i].Name() < sortedRules[j].Name()
	})

	return &ruleBasedKiller{
		defaultKiller: defaultKiller,
		rules:         sortedRules,
	}, nil
}

func (r *ruleBasedKiller) Name() string {
	return KillerNameRuleBasedKiller
}

func (r *ruleBasedKiller) Evict(ctx context.Context, pod *v1.Pod, gracePeriodSeconds int64, reason, plugin string) error {
	killer := r.defaultKiller
	for _, rule := range r.rules {
		matched, ruleKiller := rule.Match(pod)
		if !matched {
			continue
		}
		if ruleKiller == nil {
			return fmt.Errorf("killer rule %q matched nil killer", rule.Name())
		}
		killer = ruleKiller
		break
	}

	return killer.Evict(ctx, pod, gracePeriodSeconds, reason, plugin)
}
