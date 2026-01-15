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

package machine

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/safchain/ethtool"
	"github.com/vishvananda/netlink"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	MellanoxPFDriverName = "mlx5_core"
	BroadComPFDriverName = "bnxt_en"

	vfFilePrefix  = "virtfn"
	vfFilePattern = vfFilePrefix + "*"
)

func GetSriovVFList(conf *global.MachineInfoConfiguration, allNics []InterfaceInfo) ([]SriovVFInfo, error) {
	nicMap, err := getNsNicMap(conf.NetAllocatableNS, allNics)
	if err != nil {
		return nil, fmt.Errorf("failed to get pf map, err %w", err)
	}

	var vfList []SriovVFInfo

	for ns, nicList := range nicMap {
		if len(nicList) == 0 {
			continue
		}
		err := DoNetNS(ns, conf.NetNSDirAbsPath, func(sysFsDir string) error {
			for _, pf := range nicList {
				if !isSriovPf(sysFsDir, pf.Name) {
					continue
				}

				driver, err := detectSriovPFDriver(pf.Name)
				if err != nil {
					general.Warningf("cannot detect sriov pf driver for %s, err %w, skip", pf.Name, err)
					continue
				}

				var vfRepresenterMap map[int]string
				switch driver {
				case NicDriverMLX:
					vfRepresenterMap, err = getVfRepresenterMap(sysFsDir, pf.Name, "device/net")
				case NicDriverBNX:
					pfIndex, err := getBrcmPfIndex(sysFsDir, pf.PCIAddr)
					if err != nil {
						return fmt.Errorf("cannot get brcm pf index, err %w", err)
					}
					vfRepresenterMap, err = getVfRepresenterMap(sysFsDir, pf.Name, "subsystem", brcmVfRepresenterFilter(pfIndex))
				default:
					general.Warningf("not support driver type for pf %s, skip", pf.Name)
					continue
				}
				if err != nil {
					return fmt.Errorf("failed to get vf representer map, err %w", err)
				}

				vfLinkMap, err := getVfLinkMap(pf.Name)
				if err != nil {
					return fmt.Errorf("failed to get vf link map, err %w", err)
				}

				vfPCIMap, err := getVfPCIMap(sysFsDir, pf.PCIAddr)
				if err != nil {
					return fmt.Errorf("failed to get vf pci map of pf %s, err %w", pf.Name, err)
				}

				for index, pciAddr := range vfPCIMap {
					if vfLinkInfo, ok := vfLinkMap[index]; ok && vfLinkInfo.Trust != 0 {
						general.Infof("skip vf %d of pf %s, trust is %d", index, pf.Name, vfLinkInfo.Trust)
						continue
					}

					representer, ok := vfRepresenterMap[index]
					if !ok {
						general.Warningf("cannot get vf representer of pf %s with index %d", pf.Name, index)
						continue
					}

					vfList = append(vfList, SriovVFInfo{
						PFInfo:  pf,
						Index:   index,
						PCIAddr: pciAddr,
						RepName: representer,
					})
				}
			}

			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to get vf list of ns %s, err %w", ns, err)
		}
	}

	return vfList, nil
}

// isSriovPf returns true if the given interface is a sriov pf, which has num_vfs file and no physfn file
func isSriovPf(sysFsDir string, ifName string) bool {
	physfnPath := filepath.Join(sysFsDir, nicPathNAMEBaseDir, ifName, netFileNamePhysfn)
	if _, err := os.Stat(physfnPath); err == nil {
		return false
	}

	sriovPath := filepath.Join(sysFsDir, nicPathNAMEBaseDir, ifName, netFileNameNumVFS)
	if _, err := os.Stat(sriovPath); err != nil {
		return false
	}

	return true
}

func detectSriovPFDriver(ifName string) (NicDriver, error) {
	driverName, err := ethtool.DriverName(ifName)
	if err != nil {
		return NicDriverUnknown, fmt.Errorf("failed to get driver name, err %w", err)
	}

	if strings.Contains(driverName, MellanoxPFDriverName) {
		return NicDriverMLX, nil
	}

	if strings.Contains(driverName, BroadComPFDriverName) {
		return NicDriverBNX, nil
	}

	return NicDriverUnknown, nil
}

// getNsNicMap returns a map of net ns name to interface list
func getNsNicMap(netAllocatableNS []string, allNics []InterfaceInfo) (map[string][]InterfaceInfo, error) {
	allowedNS := sets.NewString(netAllocatableNS...)
	allowedNS.Insert(DefaultNICNamespace)

	pfMap := map[string][]InterfaceInfo{}
	for _, nic := range allNics {
		if !allowedNS.Has(nic.NetNSInfo.NSName) {
			continue
		}
		pfMap[nic.NetNSInfo.NSName] = append(pfMap[nic.NetNSInfo.NSName], nic)
	}

	return pfMap, nil
}

// getVfLinkMap returns a map of vf index to netlink.VfInfo
func getVfLinkMap(pfName string) (map[int]netlink.VfInfo, error) {
	link, err := netlink.LinkByName(pfName)
	if err != nil {
		return nil, fmt.Errorf("failed to get the link of pf %s, err %w", pfName, err)
	}
	attrs := link.Attrs()

	vfLinkMap := make(map[int]netlink.VfInfo)
	for _, vf := range attrs.Vfs {
		vfLinkMap[vf.ID] = vf
	}

	return vfLinkMap, nil
}

// getVfPCIMap returns a map of vf index to pci address
func getVfPCIMap(sysFsDir string, pfPCIAddr string) (map[int]string, error) {
	vfMap := make(map[int]string)

	pciBaseDirPath := filepath.Join(sysFsDir, pciPathNameBaseDir)
	vfPathPattern := filepath.Join(pciBaseDirPath, pfPCIAddr, vfFilePattern)
	vfPathList, err := filepath.Glob(vfPathPattern)
	if err != nil {
		return nil, fmt.Errorf("failed to glob %s, err %w", vfPathPattern, err)
	}

	for _, vfPath := range vfPathList {
		vfFileName := filepath.Base(vfPath)
		vfIndexStr := strings.TrimPrefix(vfFileName, vfFilePrefix)
		vfIndex, err := strconv.Atoi(vfIndexStr)
		if err != nil {
			general.Warningf("cannot parse virtfn index %s, err %w", vfIndexStr, err)
			continue
		}

		realPath, err := filepath.EvalSymlinks(vfPath)
		if err != nil {
			general.Warningf("cannot resolve symlink %s, err %w", vfPath, err)
			continue
		}
		vfPci := filepath.Base(realPath)

		vfMap[vfIndex] = vfPci
	}

	return vfMap, nil
}

// getVfRepresenterMap returns a map of vf index to representer name
func getVfRepresenterMap(sysFsDir string, pfName string, devicePath string, filters ...vfRepresenterFilter) (map[int]string, error) {
	vfRepresenterMap := make(map[int]string)

	nicsBaseDirPath := filepath.Join(sysFsDir, nicPathNAMEBaseDir)
	swIDFile := filepath.Join(nicsBaseDirPath, pfName, netDevPhysSwitchID)
	physSwitchID, err := os.ReadFile(swIDFile)
	if err != nil || string(physSwitchID) == "" {
		return nil, fmt.Errorf("cannot get switch id for pf %s, err %w", pfName, err)
	}

	pfSubsystemPath := filepath.Join(nicsBaseDirPath, pfName, devicePath)
	devices, err := os.ReadDir(pfSubsystemPath)
	if err != nil {
		return nil, fmt.Errorf("cannot get subsystem device for pf %s, err %w", pfName, err)
	}
	for _, device := range devices {
		devicePath := filepath.Join(nicsBaseDirPath, device.Name())
		deviceSwIDFile := filepath.Join(devicePath, netDevPhysSwitchID)
		deviceSwID, err := os.ReadFile(deviceSwIDFile)
		if err != nil || string(deviceSwID) != string(physSwitchID) {
			continue
		}
		devicePortNameFile := filepath.Join(devicePath, netDevPhysPortName)
		_, err = os.Stat(devicePortNameFile)
		if os.IsNotExist(err) {
			continue
		}
		physPortName, err := os.ReadFile(devicePortNameFile)
		if err != nil {
			continue
		}
		physPortNameStr := string(physPortName)
		pfRepIndex, vfRepIndex, _ := parsePortName(physPortNameStr)
		for _, filter := range filters {
			if !filter(pfRepIndex, vfRepIndex) {
				continue
			}
		}
		// At this point we're confident we have a representer.
		vfRepresenterMap[vfRepIndex] = device.Name()
	}

	return vfRepresenterMap, nil
}

var physPortRe = regexp.MustCompile(`pf(\d+)vf(\d+)`)

// parsePortName parses the phys_port_name to get pf index and vf index
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

type vfRepresenterFilter func(pfRepIndex int, vfRepIndex int) bool

// brcmVfRepresenterFilter returns a filter that only accepts vf representer of the given pf index
func brcmVfRepresenterFilter(pfIndex int) vfRepresenterFilter {
	return func(pfRepIndex int, _ int) bool {
		return pfRepIndex == pfIndex
	}
}

// getBrcmPfIndex returns the pf index of the given brcm uplink.
// The pf index for brcm nic is the index of the pf in the list of pfs with the same device id in lexical order.
func getBrcmPfIndex(sysFsDir string, pfPCIAddr string) (int, error) {
	// get the device id of the pf, e.g. 0x1750
	pciBaseDirPath := filepath.Join(sysFsDir, pciPathNameBaseDir)
	pciPath := filepath.Join(pciBaseDirPath, pfPCIAddr, "device")
	data, err := os.ReadFile(pciPath)
	if err != nil {
		return 0, fmt.Errorf("failed to get device id, err %w", err)
	}
	deviceID := strings.TrimSpace(string(data))

	// Traverse /sys/bus/pci/devices/ to find all devices with the same device ID
	pciDevicesPath := filepath.Join(sysFsDir, pciPathNameBaseDir)
	devices, err := os.ReadDir(pciDevicesPath)
	if err != nil {
		return 0, fmt.Errorf("failed to read pci devices directory, err %w", err)
	}

	var matchingDevices []string
	for _, device := range devices {
		if !device.IsDir() {
			continue
		}

		devicePath := filepath.Join(pciDevicesPath, device.Name())
		deviceIDPath := filepath.Join(devicePath, "device")
		deviceIDData, err := os.ReadFile(deviceIDPath)
		if err != nil {
			continue
		}

		currentDeviceID := strings.TrimSpace(string(deviceIDData))
		if currentDeviceID == deviceID {
			matchingDevices = append(matchingDevices, device.Name())
		}
	}

	// Sort the matching devices lexicographically
	sort.Strings(matchingDevices)

	// Find the index of the target device in the sorted list
	for i, device := range matchingDevices {
		if pfPCIAddr == device {
			return i, nil
		}
	}

	return -1, fmt.Errorf("failed to find PF index for uplink %s, device id %s", pfPCIAddr, deviceID)
}

// GetVfIBDevices returns the ib devices of the given vf name
func GetVfIBDevices(sysFsDir string, vfName string) (ibDevices []string, err error) {
	ibVerbDirPath := filepath.Join(sysFsDir, nicPathNAMEBaseDir, vfName, netFileNameIBVerbs)
	ibCMDirPath := filepath.Join(sysFsDir, nicPathNAMEBaseDir, vfName, netFileNameIBCM)
	ibMadDirPath := filepath.Join(sysFsDir, nicPathNAMEBaseDir, vfName, netFileNameIBMad)
	paths := []string{ibVerbDirPath, ibCMDirPath, ibMadDirPath}

	for _, path := range paths {
		entries, err := os.ReadDir(path)
		if err != nil {
			continue
		}

		if len(entries) == 0 {
			continue
		}

		for _, entry := range entries {
			ibDevices = append(ibDevices, entry.Name())
		}
	}

	return ibDevices, nil
}

// GetVFName returns the vf name of the given vf pci address
func GetVFName(sysFsDir string, vfPciAddress string) (string, error) {
	pciBaseDirPath := filepath.Join(sysFsDir, pciPathNameBaseDir)
	vfPath := filepath.Join(pciBaseDirPath, vfPciAddress, "net")
	if _, err := os.Stat(vfPath); os.IsNotExist(err) {
		return "", fmt.Errorf("vf net path %s does not exist", vfPath)
	}

	entries, err := os.ReadDir(vfPath)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("vf net path %s is empty", vfPath)
	}

	return entries[0].Name(), nil
}
