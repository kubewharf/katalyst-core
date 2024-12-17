package cgcpuset

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/qosgroup"
)

var (
	qosLevelToCgroupv1GroupFolder = map[qosgroup.QoSGroup]string{
		qosgroup.QoSGroupDedicated: "burstable",
		qosgroup.QoSGroupShared:    "burstable",
		qosgroup.QoSGroupReclaimed: "offline-besteffort",
		qosgroup.QoSGroupSystem:    "burstable",
	}
)

func locateCGroupV1SubFolder(qos qosgroup.QoSGroup) (string, error) {
	if folder, ok := qosLevelToCgroupv1GroupFolder[qos]; ok {
		return folder, nil
	}
	return "", fmt.Errorf("unknown cgroup v1 folder for qos group %s", qos)
}

type CPUSet struct {
	fs afero.Fs
}

func (c CPUSet) GetNumaNodes(podUID string, qos qosgroup.QoSGroup) ([]int, error) {
	cgPath, err := getCgroupCPUSetPath(podUID, qos)
	if err != nil {
		return nil, err
	}
	return getNumaNodes(c.fs, cgPath)
}

func getNumaNodes(fs afero.Fs, cpusetPath string) ([]int, error) {
	return getValues(fs, cpusetPath, "cpuset.mems")
}

func getCgroupCPUSetPath(podUID string, qosGroup qosgroup.QoSGroup) (string, error) {
	// todo: support cgroup v2
	// below assumes cgroup v1
	subFolder, err := locateCGroupV1SubFolder(qosGroup)
	if err != nil {
		return "", errors.Wrap(err, "failed to get cgroup cpuset path")
	}
	return path.Join("/sys/fs/cgroup/cpuset/kubepods/", subFolder, podUID), nil
}

func getValues(fs afero.Fs, cpusetPath string, cgfile string) ([]int, error) {
	cgPath := path.Join(cpusetPath, cgfile)
	content, err := afero.ReadFile(fs, cgPath)
	if err != nil {
		return nil, err
	}

	return parse(string(content))
}

func parse(content string) ([]int, error) {
	content = strings.TrimSpace(content)
	result := make([]int, 0)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		vs, err := parseSingleLine(line)
		if err != nil {
			return nil, err
		}
		result = append(result, vs...)
	}
	return result, nil
}

func parseSingleLine(content string) ([]int, error) {
	content = strings.TrimSpace(content)
	segs := strings.Split(content, ",")

	result := make([]int, 0)

	for _, seg := range segs {
		vs, err := parseSingleRange(seg)
		if err != nil {
			return nil, err
		}
		result = append(result, vs...)
	}

	return result, nil
}

func parseSingleRange(content string) ([]int, error) {
	content = strings.TrimSpace(content)
	index := strings.Index(content, "-")
	if index == -1 {
		v, err := parseSingleInt(content)
		if err != nil {
			return nil, err
		}
		return []int{v}, nil
	}

	left, right := content[:index], content[index+1:]
	vLeft, err := parseSingleInt(left)
	if err != nil {
		return nil, err
	}
	vRight, err := parseSingleInt(right)
	if err != nil {
		return nil, err
	}
	result := make([]int, vRight-vLeft+1)
	for i := vLeft; i <= vRight; i++ {
		result[i-vLeft] = i
	}
	return result, nil
}

func parseSingleInt(content string) (int, error) {
	content = strings.TrimSpace(content)
	return strconv.Atoi(content)
}

func New(fs afero.Fs) *CPUSet {
	return &CPUSet{
		fs: fs,
	}
}
