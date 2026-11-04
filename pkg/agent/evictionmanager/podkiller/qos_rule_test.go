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
	"fmt"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestQoSAwareRuleMatchesConfiguredQoSKiller(t *testing.T) {
	t.Parallel()

	qosKiller := &recordingRuleKiller{name: "qos"}
	rule := makeQoSAwareRule(t, generic.NewQoSConfiguration(), map[string]Killer{"qos": qosKiller}, map[string]string{
		apiconsts.PodAnnotationQoSLevelReclaimedCores: "qos",
	})

	matched, killer := rule.Match(&v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
		},
	}})
	if !matched || killer != qosKiller {
		t.Fatalf("expected qos rule to match configured reclaimed killer")
	}
}

func TestQoSAwareRuleDoesNotMatchWhenQoSKillerMissing(t *testing.T) {
	t.Parallel()

	rule := makeQoSAwareRule(t, generic.NewQoSConfiguration(), nil, nil)

	matched, killer := rule.Match(&v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
		},
	}})
	if matched || killer != nil {
		t.Fatalf("expected qos rule not to match without configured killer")
	}
}

func TestQoSAwareRuleDoesNotMatchOnQoSParseFailure(t *testing.T) {
	t.Parallel()

	qosConfig := generic.NewQoSConfiguration()
	qosConfig.QoSClassAnnotationSelector[apiconsts.PodAnnotationQoSLevelDedicatedCores]["custom-qos"] = "dedicated"
	rule := makeQoSAwareRule(t, qosConfig, map[string]Killer{"dedicated": &recordingRuleKiller{name: "dedicated"}}, map[string]string{
		apiconsts.PodAnnotationQoSLevelDedicatedCores: "dedicated",
	})

	matched, killer := rule.Match(&v1.Pod{ObjectMeta: metav1.ObjectMeta{
		Annotations: map[string]string{
			apiconsts.PodAnnotationQoSLevelKey: apiconsts.PodAnnotationQoSLevelReclaimedCores,
			"custom-qos":                       "dedicated",
		},
	}})
	if matched || killer != nil {
		t.Fatalf("expected qos parse failure to fall through to default killer")
	}
}

func makeQoSAwareRule(t *testing.T, qosConfig *generic.QoSConfiguration, killers map[string]Killer, qosPodKillers map[string]string) KillerRule {
	t.Helper()

	conf := coreconfig.NewConfiguration()
	conf.QoSConfiguration = qosConfig
	conf.QoSPodKillers = qosPodKillers
	rule, err := NewQoSAwareRule(conf, func(killerName string) (Killer, error) {
		killer, ok := killers[killerName]
		if !ok {
			return nil, fmt.Errorf("unexpected killer %q", killerName)
		}
		return killer, nil
	})
	if err != nil {
		t.Fatalf("failed to create qos rule: %v", err)
	}
	return rule
}
