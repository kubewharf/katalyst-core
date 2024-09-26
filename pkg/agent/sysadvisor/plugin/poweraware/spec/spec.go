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

package spec

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
)

type (
	PowerAlert string
	InternalOp int
)

const (
	// authentic power alert code
	PowerAlertS0 PowerAlert = "s0"
	PowerAlertP0 PowerAlert = "p0"
	PowerAlertP1 PowerAlert = "p1"
	PowerAlertP2 PowerAlert = "p2"

	// PowerAlertOK is derivative power alert code which corresponds to NON-existent power alert annotation
	PowerAlertOK PowerAlert = "ok"

	InternalOpAuto     InternalOp = 0
	InternalOpThrottle InternalOp = 1
	InternalOpEvict    InternalOp = 2
	InternalOpFreqCap  InternalOp = 4
	InternalOpPause    InternalOp = 8

	AnnoKeyPowerAlert      = "tce.kubernetes.io/power-alert"
	AnnoKeyPowerBudget     = "tce.kubernetes.io/power-budget"
	AnnoKeyPowerAlertTime  = "tce.kubernetes.io/power-alert-time"
	AnnoKeyPowerInternalOp = "tce.kubernetes.io/power-internal-op"
)

var (
	powerAlertResponseTime = map[PowerAlert]time.Duration{}
	unknownAlertError      = errors.New("unknown alert")
)

func init() {
	// todo: update response deadline settings for power alert levels in line with power management team
	powerAlertResponseTime[PowerAlertS0] = time.Minute * 2
	powerAlertResponseTime[PowerAlertP0] = time.Minute * 30
	powerAlertResponseTime[PowerAlertP1] = time.Hour * 1
	powerAlertResponseTime[PowerAlertP2] = time.Hour * 4
}

func GetPowerAlertResponseTimeLimit(alert PowerAlert) (time.Duration, error) {
	alert = PowerAlert(strings.ToLower(string(alert)))
	resp, ok := powerAlertResponseTime[alert]
	if !ok {
		return time.Duration(0), unknownAlertError
	}
	return resp, nil
}

func (o InternalOp) String() string {
	switch o {
	case InternalOpThrottle:
		return "Throttle"
	case InternalOpEvict:
		return "Evict"
	case InternalOpFreqCap:
		return "FreqCap"
	case InternalOpPause:
		return "Noop"
	default:
		return fmt.Sprintf("%d", int(o))
	}
}

type PowerSpec struct {
	Alert      PowerAlert
	Budget     int
	InternalOp InternalOp
	AlertTime  time.Time
}

func GetPowerSpec(node *v1.Node) (*PowerSpec, error) {
	alert := PowerAlert(node.Annotations[AnnoKeyPowerAlert])
	if len(alert) == 0 {
		return &PowerSpec{
			Alert:      PowerAlertOK,
			Budget:     0,
			InternalOp: 0,
		}, nil
	}

	// uniformly convert alert level input to lower case just in case
	alert = PowerAlert(strings.ToLower(string(alert)))

	// input float number like 611.31 is allowed
	budget, err := strconv.ParseFloat(node.Annotations[AnnoKeyPowerBudget], 32)
	if err != nil {
		return nil, err
	}

	internalOp := InternalOpAuto
	if len(node.Annotations[AnnoKeyPowerInternalOp]) > 0 {
		code, err := strconv.Atoi(node.Annotations[AnnoKeyPowerInternalOp])
		if err != nil {
			return nil, err
		}
		internalOp = InternalOp(code)
	}

	alertTimeStr := node.Annotations[AnnoKeyPowerAlertTime]
	alertTime, err := time.Parse(time.RFC3339, alertTimeStr)
	if err != nil {
		return nil, err
	}
	return &PowerSpec{
		Alert:      alert,
		Budget:     int(budget),
		InternalOp: internalOp,
		AlertTime:  alertTime,
	}, nil
}
