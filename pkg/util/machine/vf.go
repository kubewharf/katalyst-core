package machine

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type VFInterfaceInfo struct {
	InterfaceInfo
	// PFName is the name of the parent PF device for this VF. (e.g., eth0)
	PFName string
	// RepName is the name of the representor device for this VF. (e.g., eth0_1)
	RepName string
	// VfID The ID of the VF (e.g., 0, 1, 2...)
	VfID int
}

var physPortRe = regexp.MustCompile(`pf(\d+)vf(\d+)`)

func parsePortName(physPortName string) (pfRepIndex, vfRepIndex int, err error) {
	pfRepIndex = -1
	vfRepIndex = -1

	// old kernel syntax of phys_port_name is vf index
	physPortName = strings.TrimSpace(physPortName)
	physPortNameInt, err := strconv.Atoi(physPortName)
	if err == nil {
		vfRepIndex = physPortNameInt
	} else {
		// new kernel syntax of phys_port_name pfXVfY
		matches := physPortRe.FindStringSubmatch(physPortName)
		if len(matches) != 3 {
			err = fmt.Errorf("failed to parse physPortName %s", physPortName)
		} else {
			pfRepIndex, err = strconv.Atoi(matches[1])
			if err == nil {
				vfRepIndex, err = strconv.Atoi(matches[2])
			}
		}
	}
	return pfRepIndex, vfRepIndex, err
}
