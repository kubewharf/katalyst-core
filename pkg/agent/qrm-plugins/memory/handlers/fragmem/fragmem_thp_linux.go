//go:build linux
// +build linux

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

package fragmem

import (
	"errors"
	"fmt"
	"os"
	"strings"

	k8serrors "k8s.io/apimachinery/pkg/util/errors"

	apiconfig "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	memconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/consts"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/helper"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	procfsm "github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"
)

const (
	defaultHighOrderThreshold = 85.0

	// hysteresisRatio adds hysteresis between disable/enable thresholds to avoid frequent toggling.
	hysteresisRatio = 0.9
)

// thpEnabledPath is the sysfs path we write to when tuning THP.
// It is a var (not const) so tests can override it with a temp file.
var thpEnabledPath = procfsm.TransparentHugepageEnabledPath

// thpShmemEnabledPath is the sysfs path for tmpfs/shmem THP mode.
// It is a var (not const) so tests can override it with a temp file.
var thpShmemEnabledPath = procfsm.TransparentHugepageShmemEnabledPath

// thpDefragPath is the sysfs path for the THP defrag policy.
// It is a var (not const) so tests can override it with a temp file.
var thpDefragPath = procfsm.TransparentHugepageDefragPath

// minFreeKbytesPath is the procfs path for vm.min_free_kbytes.
// It is a var (not const) so tests can override it with a temp file.
var minFreeKbytesPath = procfsm.VMMinFreeKbytesPath

// applyTHPModeAtPath writes THP mode to sysfs.
// It is a var (not const) so tests can stub side effects around THP changes.
var applyTHPModeAtPath = procfsm.ApplyTransparentHugepageEnabledAtPath

type thpDecision int

const (
	thpDecisionNone thpDecision = iota
	thpDecisionDisable
	thpDecisionEnable
)

// SetMemTHP periodically applies an explicitly configured defrag policy and tunes host THP modes.
func SetMemTHP(conf *coreconfig.Configuration,
	_ interface{}, dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter, metaServer *metaserver.MetaServer,
) {
	var errList []error
	defer func() {
		_ = general.UpdateHealthzStateByError(memconsts.SetMemTHP, k8serrors.NewAggregate(errList))
	}()

	if conf == nil {
		err := fmt.Errorf("SetMemTHP: nil configuration")
		errList = append(errList, err)
		general.Errorf("%v", err)
		return
	}
	fragMemConf := getFragMemConfiguration(dynamicConf)
	if fragMemConf == nil {
		general.Infof("SetMemTHP skipped: dynamic fragmem configuration not found")
		return
	}

	if !fragMemConf.EnableFragMem {
		general.Infof("SetMemTHP skipped: EnableFragMem disabled")
		return
	}
	general.Infof("SetMemTHP: EnableFragMem enabled")

	// An explicitly configured defrag policy is independent of static and fragmentation-based THP mode tuning.
	if err := setTHPDefragPolicyAtPath(thpDefragPath, fragMemConf.THPDefragPolicy); err != nil {
		errList = append(errList, err)
	}

	if enabled, thpMode, thpShmMode := getStaticTHPModes(fragMemConf); enabled {
		general.Infof("SetMemTHP: static THP config enabled, thp=%s thpShm=%s", thpMode, thpShmMode)
		if err := setTHPMode(thpMode, thpShmMode); err != nil {
			errList = append(errList, err)
		}
		return
	}

	mode := fragMemConf.THPDefaultConfig
	// If THPDefaultConfig is empty, skip THP mode tuning.
	if mode == "" {
		general.Infof("SetMemTHP: skip THP mode tuning, THPDefaultConfig is empty")
		return
	}
	// If THPDefaultConfig is "never", fast-path to disable THP directly.
	if mode == apiconfig.THPModeNever {
		general.Infof("SetMemTHP: THPDefaultConfig=never, disable THP directly")
		if err := setTHPMode(apiconfig.THPModeNever, apiconfig.THPShmModeDeny); err != nil {
			errList = append(errList, err)
		}
		return
	}

	if emitter == nil || metaServer == nil {
		err := fmt.Errorf("SetMemTHP: nil input, emitter=%T metaServer=%T", emitter, metaServer)
		errList = append(errList, err)
		general.Errorf("%v", err)
		return
	}

	if err := doMemTHP(fragMemConf, metaServer, emitter); err != nil {
		errList = append(errList, err)
	}
}

func getStaticTHPModes(conf *dynamicqrm.FragMemConfiguration) (bool, apiconfig.THPMode, apiconfig.THPShmMode) {
	if conf == nil || conf.THPStaticEnableConfig == nil || !conf.THPStaticEnableConfig.Enable {
		return false, "", ""
	}

	thpMode := conf.THPStaticEnableConfig.THP
	if thpMode == "" {
		thpMode = apiconfig.THPModeMadvise
	}

	thpShmMode := conf.THPStaticEnableConfig.THPShm
	if thpShmMode == "" {
		thpShmMode = apiconfig.THPShmModeAdvise
	}

	return true, thpMode, thpShmMode
}

func doMemTHP(conf *dynamicqrm.FragMemConfiguration, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) error {
	if metaServer == nil || emitter == nil {
		return nil
	}

	threshold := getHighOrderThreshold(conf)
	enableThreshold := threshold * hysteresisRatio

	maxScore := -1.0
	maxNumaID := -1
	var missingScore int
	var validNUMACnt int

	for _, numaID := range metaServer.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		// This metric is produced by Malachite metrics provisioner from extfrag.mem_order_scores.
		metricData, err := helper.GetNumaMetricWithTime(metaServer.MetricsFetcher, emitter, consts.MetricMemFragHighOrderScoreNuma, numaID)
		if err != nil {
			missingScore++
			general.Infof("THP extfrag highOrderScore missing numa=%d, err=%v", numaID, err)
			continue
		}

		highOrderScore := metricData.Value
		general.Infof("THP extfrag numa=%d highOrderScore=%.1f", numaID, highOrderScore)
		validNUMACnt++
		if highOrderScore > maxScore {
			maxScore = highOrderScore
			maxNumaID = numaID
		}
	}

	if missingScore > 0 {
		general.Infof("THP extfrag highOrderScore missing on %d NUMA nodes", missingScore)
	}

	decision := decideTHPDecision(maxScore, threshold)
	switch decision {
	case thpDecisionDisable:
		general.Infof("THP disable triggered: maxHighOrderScore=%.1f numa=%d threshold=%.1f", maxScore, maxNumaID, threshold)
		return setTHPMode(apiconfig.THPModeNever, apiconfig.THPShmModeDeny)
	case thpDecisionEnable:
		// Be conservative: only try to recover when we have valid scores for all NUMA nodes.
		// If metrics are missing on any NUMA node, keep current THP mode unchanged.
		if validNUMACnt > 0 && missingScore == 0 {
			mode := conf.THPDefaultConfig
			if mode == "" {
				mode = apiconfig.THPModeMadvise
			}
			general.Infof("THP enable triggered: maxHighOrderScore=%.1f enableThreshold=%.1f threshold=%.1f recoverTo=%s shmemRecoverTo=%s", maxScore, enableThreshold, threshold, mode, apiconfig.THPShmModeAdvise)
			return setTHPMode(mode, apiconfig.THPShmModeAdvise)
		}
		general.Infof("THP enable skipped due to missing metrics: maxHighOrderScore=%.1f enableThreshold=%.1f threshold=%.1f missingScore=%d", maxScore, enableThreshold, threshold, missingScore)
		return nil
	default:
		// Keep current mode to avoid flapping between disable/enable.
		return nil
	}
}

func getHighOrderThreshold(conf *dynamicqrm.FragMemConfiguration) float64 {
	if conf == nil {
		return defaultHighOrderThreshold
	}

	val := float64(conf.THPHighOrderScoreThreshold)
	if val <= 0 {
		return defaultHighOrderThreshold
	}
	// Clamp to a reasonable range.
	return general.Clamp(val, 1, 100)
}

func decideTHPDecision(maxScore, threshold float64) thpDecision {
	if threshold <= 0 {
		threshold = defaultHighOrderThreshold
	}

	// Use two thresholds to avoid flapping:
	// - Disable threshold: maxScore > threshold
	// - Enable threshold:  maxScore < threshold*hysteresisRatio
	if maxScore > threshold {
		return thpDecisionDisable
	}
	if maxScore >= 0 && maxScore < threshold*hysteresisRatio {
		return thpDecisionEnable
	}
	return thpDecisionNone
}

func setTHPMode(enabledMode apiconfig.THPMode, shmemMode apiconfig.THPShmMode) error {
	var errList []error

	if err := setTHPModeAtPath(thpEnabledPath, enabledMode); err != nil {
		errList = append(errList, err)
	}
	if err := setTHPModeAtPathIfExists(thpShmemEnabledPath, shmemMode); err != nil {
		errList = append(errList, err)
	}

	return k8serrors.NewAggregate(errList)
}

func setTHPModeAtPathIfExists[T ~string](path string, mode T) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			general.Infof("THP path %s not found, skip applying mode %q", path, mode)
			return nil
		}
		return fmt.Errorf("stat THP setting file %s failed: %w", path, err)
	}
	return setTHPModeAtPath(path, mode)
}

func setTHPModeAtPath[T ~string](path string, mode T) error {
	normalizedMode := normalizeTHPMode(mode)
	normalizedModeStr := string(normalizedMode)
	if !isValidTHPMode(normalizedModeStr) {
		return fmt.Errorf("invalid THP mode %q, expected one of %q/%q/%q/%q/%q/%q/%q/%q/%q", normalizedMode,
			apiconfig.THPShmModeAlways, apiconfig.THPShmModeWithinSize, apiconfig.THPShmModeAdvise,
			apiconfig.THPShmModeNever, apiconfig.THPShmModeDeny, apiconfig.THPShmModeForce,
			apiconfig.THPModeMadvise, apiconfig.THPModeAlways, apiconfig.THPModeNever)
	}

	return writeTHPSettingAtPath(path, normalizedModeStr)
}

func setTHPDefragPolicyAtPath(path string, policy apiconfig.THPDefragPolicy) error {
	normalizedPolicy, err := dynamicqrm.ParseTHPDefragPolicy(string(policy))
	if err != nil {
		return err
	}
	if normalizedPolicy == "" {
		return nil
	}

	return writeTHPSettingAtPath(path, string(normalizedPolicy))
}

// writeTHPSettingAtPath writes a validated value, preserving idempotence and
// the existing vm.min_free_kbytes handling when changing the THP enabled mode.
func writeTHPSettingAtPath(path, value string) (retErr error) {
	content, err := procfsm.ReadFileNoStat(path)
	if err != nil {
		return fmt.Errorf("read THP setting file %s failed: %w", path, err)
	}
	current, err := parseTHPActiveValue(string(content))
	if err != nil {
		return fmt.Errorf("read THP setting file %s failed: %w", path, err)
	}
	if current == value {
		general.Infof("THP setting already %q, skip writing to %s", value, path)
		return nil
	}

	if shouldPreserveMinFreeKbytes(path, value) {
		restoreMinFreeKbytes, err := saveMinFreeKbytes()
		if err != nil {
			return err
		}
		defer func() {
			if err := restoreMinFreeKbytes(); err != nil {
				if retErr == nil {
					retErr = err
				} else {
					retErr = k8serrors.NewAggregate([]error{retErr, err})
				}
			}
		}()
	}

	if err := applyTHPModeAtPath(path, value); err != nil {
		return fmt.Errorf("set THP setting failed, write %q to %s: %w", value, path, err)
	}

	newContent, err := procfsm.ReadFileNoStat(path)
	if err != nil {
		return fmt.Errorf("read back THP setting file %s failed: %w", path, err)
	}
	active, err := parseTHPActiveValue(string(newContent))
	if err != nil {
		return fmt.Errorf("set THP setting verification failed at %s: %w", path, err)
	}
	if active != value {
		return fmt.Errorf("set THP setting verification failed: wrote=%q active=%q path=%s", value, active, path)
	}

	general.Infof("THP setting set to %q at %s", value, path)
	return nil
}

// parseTHPActiveValue reads the bracketed selection from a sysfs option list.
// A single bare value is also accepted for ordinary files used by tests.
func parseTHPActiveValue(content string) (string, error) {
	fields := strings.Fields(content)
	if len(fields) == 1 && !strings.ContainsAny(fields[0], "[]") {
		return fields[0], nil
	}

	var active string
	for _, field := range fields {
		if !strings.ContainsAny(field, "[]") {
			continue
		}
		if active != "" || !strings.HasPrefix(field, "[") || !strings.HasSuffix(field, "]") ||
			len(field) <= 2 || strings.ContainsAny(field[1:len(field)-1], "[]") {
			return "", fmt.Errorf("invalid THP setting content %q", strings.TrimSpace(content))
		}
		active = field[1 : len(field)-1]
	}
	if active == "" {
		return "", fmt.Errorf("missing active THP setting in %q", strings.TrimSpace(content))
	}
	return active, nil
}

func shouldPreserveMinFreeKbytes(path, mode string) bool {
	return path == thpEnabledPath && mode == string(normalizeTHPMode(apiconfig.THPModeMadvise))
}

func saveMinFreeKbytes() (func() error, error) {
	content, err := procfsm.ReadFileNoStat(minFreeKbytesPath)
	if err != nil {
		return nil, fmt.Errorf("read vm.min_free_kbytes %s failed: %w", minFreeKbytesPath, err)
	}

	saved := append([]byte(nil), content...)
	savedDisplay := strings.TrimSpace(string(saved))
	general.Infof("saved vm.min_free_kbytes=%q before setting THP to %s", savedDisplay, apiconfig.THPModeMadvise)

	return func() error {
		if err := os.WriteFile(minFreeKbytesPath, saved, 0o644); err != nil {
			return fmt.Errorf("restore vm.min_free_kbytes %s to %q failed: %w", minFreeKbytesPath, savedDisplay, err)
		}
		general.Infof("restored vm.min_free_kbytes=%q after setting THP to %s", savedDisplay, apiconfig.THPModeMadvise)
		return nil
	}, nil
}

func normalizeTHPMode[T ~string](mode T) apiconfig.THPMode {
	return apiconfig.THPMode(strings.TrimSpace(strings.ToLower(string(mode))))
}

func isValidTHPMode(mode string) bool {
	switch mode {
	case string(apiconfig.THPShmModeAlways), string(apiconfig.THPShmModeWithinSize),
		string(apiconfig.THPShmModeAdvise), string(apiconfig.THPShmModeNever),
		string(apiconfig.THPShmModeDeny), string(apiconfig.THPShmModeForce),
		string(apiconfig.THPModeMadvise):
		return true
	default:
		return false
	}
}
