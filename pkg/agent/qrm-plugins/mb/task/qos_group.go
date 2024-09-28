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

package task

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	resctrlconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
)

const (
	QoSGroupDedicated QoSGroup = resctrlconsts.GroupDedicated
	QoSGroupShared    QoSGroup = resctrlconsts.GroupSharedCore
	QoSGroupSystem    QoSGroup = resctrlconsts.GroupSystem
	QoSGroupReclaimed QoSGroup = resctrlconsts.GroupReclaimed
)

var qosLevelToQoSGroup = map[consts.QoSLevel]QoSGroup{
	consts.QoSLevelDedicatedCores: resctrlconsts.GroupDedicated,
	consts.QoSLevelSharedCores:    resctrlconsts.GroupSharedCore,
	consts.QoSLevelReclaimedCores: resctrlconsts.GroupReclaimed,
	consts.QoSLevelSystemCores:    resctrlconsts.GroupSystem,
}

var (
	QoSReclaimed = &QoS{
		Level: consts.QoSLevelReclaimedCores,
	}

	QoSDedicated = &QoS{
		Level: consts.QoSLevelDedicatedCores,
	}

	QoSSystem = &QoS{
		Level: consts.QoSLevelSystemCores,
	}

	// QoSShared50 is for normal micro services
	QoSShared50 = &QoS{
		Level:    consts.QoSLevelSharedCores,
		SubLevel: 50,
	}

	// QoSShared30 is for batch yodel pods
	QoSShared30 = &QoS{
		Level:    consts.QoSLevelSharedCores,
		SubLevel: 30,
	}
)

var (
	qosLevelToCgroupv1GroupFolder = map[consts.QoSLevel]string{
		consts.QoSLevelDedicatedCores: "burstable",
		consts.QoSLevelSharedCores:    "burstable",
		consts.QoSLevelReclaimedCores: "offline-besteffort",
		consts.QoSLevelSystemCores:    "burstable",
	}
)

// QoSGroup supports shared_XX qos spec, besides dedicated, system and reclaimed
// QoSGroup in /pkg/agent/qrm-plugins/mb/resctrl/consts is not directly used here
// as it has difficulty to represent shared_XX properly
type QoSGroup string

// QoS represents qos value of QoSGroup
type QoS struct {
	Level    consts.QoSLevel
	SubLevel int
}

func (q QoS) ToCtrlGroup() (QoSGroup, error) {
	if q.Level != consts.QoSLevelSharedCores {
		if ctrlGroup, ok := qosLevelToQoSGroup[q.Level]; ok {
			return ctrlGroup, nil
		}

		return "", fmt.Errorf("unrecognized qos level %s", q.Level)
	}

	return QoSGroup(fmt.Sprintf("shared_%02d", q.SubLevel)), nil
}

func parseSharedSubGroup(ctrlGroup string) (int, error) {
	if !strings.HasPrefix(string(ctrlGroup), "shared_") {
		return -1, fmt.Errorf("unrecognied qos ctrl group %q", ctrlGroup)
	}

	digits := strings.TrimPrefix(string(ctrlGroup), "shared_")
	value, err := strconv.Atoi(digits)
	if err != nil {
		return -1, errors.Wrap(err, "failed to parse shared sub ctrl group")
	}

	return value, nil
}

func NewQoS(ctrlGroup QoSGroup) (*QoS, error) {
	switch ctrlGroup {
	case QoSGroupDedicated:
		return QoSDedicated, nil
	case QoSGroupSystem:
		return QoSSystem, nil
	case QoSGroupReclaimed:
		return QoSReclaimed, nil
	}

	subGroup, err := parseSharedSubGroup(string(ctrlGroup))
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse shared sub ctrl group")
	}

	switch subGroup {
	case 50:
		return QoSShared50, nil
	case 30:
		return QoSShared30, nil
	default:
		return &QoS{
			Level:    consts.QoSLevelSharedCores,
			SubLevel: subGroup,
		}, nil
	}
}
