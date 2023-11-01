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

package util

import (
	"fmt"
	"hash/crc32"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
)

const (
	baselineCoefficientSep = ","
)

type BaselineCoefficient []int64

func (c BaselineCoefficient) Cmp(c1 BaselineCoefficient) int {
	for i := 0; i < len(c); i++ {
		if len(c1) <= i {
			break
		}

		if c[i] < c1[i] {
			return -1
		} else if c[i] > c1[i] {
			return 1
		}
	}
	return 0
}

func (c BaselineCoefficient) String() string {
	s := make([]string, 0, len(c))
	for i := range c {
		s = append(s, strconv.Itoa(int(c[i])))
	}

	return strings.Join(s, baselineCoefficientSep)
}

// IsBaselinePod check whether a pod is baseline pod and whether
// the spd baseline is enabled
func IsBaselinePod(pod *v1.Pod, spd *v1alpha1.ServiceProfileDescriptor) (bool, bool, error) {
	if pod == nil || spd == nil {
		return false, false, fmt.Errorf("pod or spd is nil")
	}

	// if spd baseline ratio not config means baseline is disabled
	if spd.Spec.BaselineRatio == nil {
		return false, false, nil
	}

	bp, err := GetSPDBaselinePercentile(spd)
	if err != nil {
		return false, false, err
	}

	bc := GetPodBaselineCoefficient(pod)
	if bc.Cmp(bp) <= 0 {
		return true, true, nil
	}

	return false, true, nil
}

// GetPodBaselineCoefficient get the baseline coefficient of this pod
func GetPodBaselineCoefficient(pod *v1.Pod) BaselineCoefficient {
	if pod == nil {
		return nil
	}

	return BaselineCoefficient{
		pod.CreationTimestamp.Unix(),
		int64(crc32.ChecksumIEEE([]byte(pod.Name))),
	}
}

// GetSPDBaselinePercentile get the baseline percentile of this spd
func GetSPDBaselinePercentile(spd *v1alpha1.ServiceProfileDescriptor) (BaselineCoefficient, error) {
	if spd == nil {
		return nil, nil
	}

	s, ok := spd.Annotations[consts.SPDAnnotationBaselinePercentileKey]
	if !ok {
		return nil, fmt.Errorf("spd baseline percentile not found")
	}

	return parseBaselineCoefficient(s)
}

// SetSPDBaselinePercentile set the baseline percentile of this spd, if percentile is nil means delete it
func SetSPDBaselinePercentile(spd *v1alpha1.ServiceProfileDescriptor, percentile *BaselineCoefficient) {
	if spd == nil {
		return
	}

	if percentile == nil {
		delete(spd.Annotations, consts.SPDAnnotationBaselinePercentileKey)
		return
	}

	if spd.Annotations == nil {
		spd.Annotations = make(map[string]string)
	}

	spd.Annotations[consts.SPDAnnotationBaselinePercentileKey] = percentile.String()
	return
}

func parseBaselineCoefficient(str string) (BaselineCoefficient, error) {
	var errList []error
	s := strings.Split(str, baselineCoefficientSep)
	c := make([]int64, 0, len(s))
	for i := range s {
		v, err := strconv.Atoi(s[i])
		if err != nil {
			errList = append(errList, fmt.Errorf("index %d cannot parse: %v", i, err))
			continue
		}
		c = append(c, int64(v))
	}

	if len(errList) > 0 {
		return nil, errors.NewAggregate(errList)
	}
	return c, nil
}
