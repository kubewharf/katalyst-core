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
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	bandwidthScopePrefix            = "bandwidth"
	bandwidthDirectionRX            = "rx"
	bandwidthDirectionTX            = "tx"
	bandwidthPressureStateRetention = time.Minute
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

// bandwidthPressureState stores bandwidth samples for one (netns, nic, direction).
// Threshold hits are calculated with the current dynamic threshold when evaluated.
type bandwidthPressureState struct {
	ring        []bandwidthSample
	nextIndex   int
	sampleCount int
}

// BandwidthMetricQuerier isolates metric access from eviction decisions.
// It returns runtime usage only; CNR remains the capacity source.
type BandwidthMetricQuerier interface {
	GetBandwidthMetric(scope bandwidthScope) (float64, bool)
	GetPodDirectionUsage(pod *v1.Pod, direction string) (float64, bool)
}

// metaServerBandwidthMetricQuerier reads instantaneous usage only; NIC capacity
// comes from CNR so bandwidth eviction shares the same source as allocation.
type metaServerBandwidthMetricQuerier struct {
	metaServer *metaserver.MetaServer
}

func newMetaServerBandwidthMetricQuerier(metaServer *metaserver.MetaServer) BandwidthMetricQuerier {
	if metaServer == nil || metaServer.MetricsFetcher == nil {
		return nil
	}

	return &metaServerBandwidthMetricQuerier{metaServer: metaServer}
}

func (m *metaServerBandwidthMetricQuerier) GetBandwidthMetric(scope bandwidthScope) (float64, bool) {
	if m == nil || m.metaServer == nil || m.metaServer.MetricsFetcher == nil {
		return 0, false
	}

	metricName := coreconsts.MetricNetReceiveBPS
	if scope.direction == bandwidthDirectionTX {
		metricName = coreconsts.MetricNetTransmitBPS
	}

	bps, err := m.metaServer.MetricsFetcher.GetNSNetworkMetric(scope.netns, scope.nic, metricName)
	if err != nil {
		return 0, false
	}

	return bps.Value, true
}

func (m *metaServerBandwidthMetricQuerier) GetPodDirectionUsage(pod *v1.Pod, direction string) (float64, bool) {
	if m == nil || m.metaServer == nil || m.metaServer.MetricsFetcher == nil || pod == nil {
		return 0, false
	}

	metricName := coreconsts.MetricNetTcpRecvBPSContainer
	if direction == bandwidthDirectionTX {
		metricName = coreconsts.MetricNetTcpSendBPSContainer
	}

	total := 0.0
	found := false
	for _, container := range pod.Spec.Containers {
		metric, err := m.metaServer.MetricsFetcher.GetContainerMetric(string(pod.UID), container.Name, metricName)
		if err != nil {
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

func (s *bandwidthPressureState) observe(bps, capacityMbps float64, now time.Time) {
	if s == nil || len(s.ring) == 0 {
		return
	}

	s.ring[s.nextIndex] = bandwidthSample{
		bps:          bps,
		capacityMbps: capacityMbps,
		observedAt:   now,
	}
	s.nextIndex = (s.nextIndex + 1) % len(s.ring)
	if s.sampleCount < len(s.ring) {
		s.sampleCount++
	}
}

func (s *bandwidthPressureState) expired(now time.Time, retention time.Duration) bool {
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
	return now.Sub(latestSample.observedAt) > retention
}

func (s *bandwidthPressureState) evaluate(threshold float64) bandwidthPressureEvaluation {
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

func (s *bandwidthPressureState) latestSample() (bandwidthSample, bool) {
	if s == nil || len(s.ring) == 0 || s.sampleCount == 0 {
		return bandwidthSample{}, false
	}
	index := (s.nextIndex - 1 + len(s.ring)) % len(s.ring)
	return s.ring[index], true
}

func (e bandwidthPressureEvaluation) met(continuousThreshold, ringMetThreshold int) bool {
	if continuousThreshold > 0 && e.consecutiveHits >= continuousThreshold {
		return true
	}
	if ringMetThreshold > 0 && e.currentHits >= ringMetThreshold {
		return true
	}
	return false
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
