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

package network

import (
	"fmt"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	coreconsts "github.com/kubewharf/katalyst-core/pkg/consts"
	metrictypes "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	utilmetric "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

const (
	bandwidthScopePrefix            = "bandwidth"
	bandwidthDirectionRX            = "rx"
	bandwidthDirectionTX            = "tx"
	bandwidthPressureStateRetention = 5 * time.Minute
	bitsPerByte                     = 8
	bpsPerMbps                      = 1000 * 1000
)

type bandwidthScope struct {
	netns     string
	nic       string
	direction string
}

type bandwidthSample struct {
	bps          float64
	capacityMbps float64
	observedAt   time.Time
}

type bandwidthPressureEvaluation struct {
	lastUtilization float64
	consecutiveHits int
	currentHits     int
}

// bandwidthState stores healthy NIC capacity and the sampled bandwidth history
// for one (netns, nic, direction). Threshold hits are calculated when evaluated.
type bandwidthState struct {
	ring        []bandwidthSample
	nextIndex   int
	sampleCount int
}

// BandwidthMetricQuerier isolates metric access from eviction decisions.
// It returns runtime usage only; CNR remains the capacity source.
type BandwidthMetricQuerier interface {
	GetBandwidthMetric(scope bandwidthScope) (utilmetric.MetricData, error)
	GetPodDirectionUsage(pod *v1.Pod, direction string) (float64, bool)
}

// bandwidthMetricQuerier reads instantaneous usage only; NIC capacity
// comes from CNR so bandwidth eviction shares the same source as allocation.
type bandwidthMetricQuerier struct {
	metrictypes.MetricsFetcher
}

func newBandwidthMetricQuerier(metaServer metrictypes.MetricsFetcher) BandwidthMetricQuerier {
	return &bandwidthMetricQuerier{metaServer}
}

func (m *bandwidthMetricQuerier) GetBandwidthMetric(scope bandwidthScope) (utilmetric.MetricData, error) {
	metricName := coreconsts.MetricNetReceiveBPS
	if scope.direction == bandwidthDirectionTX {
		metricName = coreconsts.MetricNetTransmitBPS
	}

	return m.GetNSNetworkMetric(scope.netns, scope.nic, metricName)
}

func (m *bandwidthMetricQuerier) GetPodDirectionUsage(pod *v1.Pod, direction string) (float64, bool) {
	metricName := coreconsts.MetricNetTcpRecvBPSContainer
	if direction == bandwidthDirectionTX {
		metricName = coreconsts.MetricNetTcpSendBPSContainer
	}

	total := 0.0
	found := false
	for _, container := range pod.Spec.Containers {
		metric, err := m.MetricsFetcher.GetContainerMetric(string(pod.UID), container.Name, metricName)
		if err != nil {
			general.Errorf("failed to get container metric %s for pod %s: %v", metricName, pod.Name, err)
			continue
		}
		total += metric.Value
		found = true
	}

	return total, found
}

func formatBandwidthScope(scope bandwidthScope) string {
	return fmt.Sprintf("%s/%s/%s/%s", bandwidthScopePrefix, scope.netns, scope.nic, scope.direction)
}

func parseBandwidthScope(raw string) (bandwidthScope, error) {
	parts := strings.Split(raw, "/")
	if len(parts) != 4 || parts[0] != bandwidthScopePrefix {
		return bandwidthScope{}, fmt.Errorf("invalid bandwidth scope %q", raw)
	}
	if parts[2] == "" {
		return bandwidthScope{}, fmt.Errorf("invalid bandwidth scope %q", raw)
	}
	if parts[3] != bandwidthDirectionRX && parts[3] != bandwidthDirectionTX {
		return bandwidthScope{}, fmt.Errorf("invalid bandwidth direction %q", parts[3])
	}

	return bandwidthScope{
		netns:     parts[1],
		nic:       parts[2],
		direction: parts[3],
	}, nil
}

func convertMbpsToBytesPerSecond(mbps float64) float64 {
	return mbps * bpsPerMbps / bitsPerByte
}

func getPodBandwidthScope(pod *v1.Pod, target bandwidthScope) (bandwidthScope, bool) {
	if pod.Annotations != nil {
		if identifier := pod.Annotations[apiconsts.PodAnnotationNICSelectionResultKey]; identifier != "" {
			netns, nic, ok := machine.ParseNICIdentifier(identifier)
			if !ok {
				return bandwidthScope{}, false
			}
			return bandwidthScope{netns: netns, nic: nic}, true
		}
	}
	if target.netns == machine.DefaultNICNamespace {
		return bandwidthScope{netns: machine.DefaultNICNamespace, nic: target.nic}, true
	}
	return bandwidthScope{}, false
}

func (s *bandwidthState) observe(sample *bandwidthSample) {
	if sample == nil {
		return
	}

	s.ring[s.nextIndex] = bandwidthSample{
		bps:          sample.bps,
		capacityMbps: sample.capacityMbps,
		observedAt:   sample.observedAt,
	}
	s.nextIndex = (s.nextIndex + 1) % len(s.ring)
	if s.sampleCount < len(s.ring) {
		s.sampleCount++
	}
}

func (s *bandwidthState) expired(retention time.Duration) bool {
	if s == nil {
		return true
	}
	if retention <= 0 {
		return false
	}
	latestSample, ok := s.latestSample()
	if !ok || latestSample.observedAt.IsZero() {
		return true
	}
	return time.Now().Sub(latestSample.observedAt) > retention
}

func (s *bandwidthState) evaluate(threshold float64) bandwidthPressureEvaluation {
	if s == nil || len(s.ring) == 0 || s.sampleCount == 0 {
		return bandwidthPressureEvaluation{}
	}

	evaluation := bandwidthPressureEvaluation{}
	if latestSample, ok := s.latestSample(); ok {
		latestCapacityBPS := convertMbpsToBytesPerSecond(latestSample.capacityMbps)
		if latestCapacityBPS > 0 {
			evaluation.lastUtilization = latestSample.bps / latestCapacityBPS
		}
	}
	for i := 0; i < s.sampleCount; i++ {
		sample := s.ring[i]
		if sample.observedAt.IsZero() {
			continue
		}
		capacityBPS := convertMbpsToBytesPerSecond(sample.capacityMbps)
		if capacityBPS > 0 && sample.bps/capacityBPS >= threshold {
			evaluation.currentHits++
		}
	}
	for i := 0; i < s.sampleCount; i++ {
		index := (s.nextIndex - 1 - i + len(s.ring)) % len(s.ring)
		sample := s.ring[index]
		capacityBPS := convertMbpsToBytesPerSecond(sample.capacityMbps)
		if sample.observedAt.IsZero() || capacityBPS <= 0 || sample.bps/capacityBPS < threshold {
			break
		}
		evaluation.consecutiveHits++
	}
	return evaluation
}

func (s *bandwidthState) met(utilizationThreshold float64, continuousThreshold, ringMetThreshold int) (bandwidthPressureEvaluation, bool) {
	evaluation := s.evaluate(utilizationThreshold)
	if continuousThreshold > 0 && evaluation.consecutiveHits >= continuousThreshold {
		return evaluation, true
	}
	if ringMetThreshold > 0 && evaluation.currentHits >= ringMetThreshold {
		return evaluation, true
	}
	return evaluation, false
}

func (s *bandwidthState) latestSample() (bandwidthSample, bool) {
	if s == nil || len(s.ring) == 0 || s.sampleCount == 0 {
		return bandwidthSample{}, false
	}
	index := (s.nextIndex - 1 + len(s.ring)) % len(s.ring)
	return s.ring[index], true
}

func isBandwidthPressureMoreSevere(scope bandwidthScope, evaluation bandwidthPressureEvaluation, bestScope bandwidthScope, bestEvaluation bandwidthPressureEvaluation) bool {
	if evaluation.lastUtilization != bestEvaluation.lastUtilization {
		return evaluation.lastUtilization > bestEvaluation.lastUtilization
	}
	if evaluation.currentHits != bestEvaluation.currentHits {
		return evaluation.currentHits > bestEvaluation.currentHits
	}
	if evaluation.consecutiveHits != bestEvaluation.consecutiveHits {
		return evaluation.consecutiveHits > bestEvaluation.consecutiveHits
	}
	if scope.direction != bestScope.direction {
		return scope.direction == bandwidthDirectionRX
	}
	return formatBandwidthScope(scope) < formatBandwidthScope(bestScope)
}
