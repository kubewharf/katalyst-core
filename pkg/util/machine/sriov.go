package machine

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/safchain/ethtool"
	"k8s.io/klog/v2"
)

var virtFnRe = regexp.MustCompile(`virtfn(\d+)`)

func GetVfIndexByPciAddress(sysFsDir string, vfPciAddress string) (int, error) {
	pciBaseDirPath := path.Join(sysFsDir, pciPathNameBaseDir)
	vfPath := filepath.Join(pciBaseDirPath, vfPciAddress, "physfn", "virtfn*")
	matches, err := filepath.Glob(vfPath)
	if err != nil {
		return -1, err
	}
	for _, match := range matches {
		tmp, err := os.Readlink(match)
		if err != nil {
			continue
		}
		if strings.Contains(tmp, vfPciAddress) {
			result := virtFnRe.FindStringSubmatch(match)
			vfIndex, err := strconv.Atoi(result[1])
			if err != nil {
				continue
			}
			return vfIndex, nil
		}
	}
	return -1, fmt.Errorf("vf index for %s not found", vfPciAddress)
}

func GetUplinkRepresentor(sysFsDir string, pciAddress string) (string, error) {
	devicePath := filepath.Join(sysFsDir, pciPathNameBaseDir, pciAddress, "physfn", "net")
	devices, err := os.ReadDir(devicePath)
	if err != nil {
		return "", fmt.Errorf("failed to lookup %s: %v", pciAddress, err)
	}
	for _, device := range devices {
		if isSwitchdev(sysFsDir, device.Name()) {
			return device.Name(), nil
		}
	}
	return "", fmt.Errorf("uplink for %s not found", pciAddress)
}

func isSwitchdev(sysFsDir string, netdevice string) bool {
	swIDFile := filepath.Join(sysFsDir, nicPathNAMEBaseDir, netdevice, netDevPhysSwitchID)
	physSwitchID, err := os.ReadFile(swIDFile)
	if err != nil {
		klog.Infof("failed to read file %s, err %s", swIDFile, err.Error())
		return false
	}
	if physSwitchID != nil && string(physSwitchID) != "" {
		return true
	}
	return false
}

func GetVfRepresentor(sysFsDir string, uplink string, vfIndex int) (string, error) {
	driver, err := hardwareDriverDetect(uplink)
	if err != nil {
		klog.Infof("get driver info failed,if %s, error: %s", uplink, err.Error())
		return "", err
	}
	switch driver {
	case MellanoxDriverName:
		return getMlnxVfRepresentor(sysFsDir, uplink, vfIndex)
	case BroadComDriverName:
		return getBrcmVfRepresentor(sysFsDir, uplink, vfIndex)
	default:
		return "", fmt.Errorf("unsupported driver type %s", driver)
	}
}

func getBrcmVfRepresentor(sysFsDir string, uplink string, vfIndex int) (string, error) {
	nicsBaseDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir)
	swIDFile := filepath.Join(nicsBaseDirPath, uplink, netDevPhysSwitchID)
	physSwitchID, err := os.ReadFile(swIDFile)
	if err != nil || string(physSwitchID) == "" {
		return "", fmt.Errorf("cant get uplink %s switch id", uplink)
	}

	pfSubsystemPath := filepath.Join(nicsBaseDirPath, uplink, "subsystem")
	devices, err := os.ReadDir(pfSubsystemPath)
	if err != nil {
		return "", err
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
		if pfRepIndex != -1 {
			pfIndex, err := getBrcmPfIndex(sysFsDir, uplink)
			if err != nil {
				klog.Infof("failed to get brcm pf index, %s", err.Error())
				continue
			}
			if pfRepIndex != pfIndex {
				continue
			}
		}
		// At this point we're confident we have a representor.
		if vfRepIndex == vfIndex {
			return device.Name(), nil
		}
	}
	return "", fmt.Errorf("failed to find VF representor for uplink %s", uplink)
}

func getMlnxVfRepresentor(sysFsDir string, uplink string, vfIndex int) (string, error) {
	nicsBaseDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir)

	swIDFile := filepath.Join(nicsBaseDirPath, uplink, netDevPhysSwitchID)
	physSwitchID, err := os.ReadFile(swIDFile)
	if err != nil || string(physSwitchID) == "" {
		return "", fmt.Errorf("cant get uplink %s switch id", uplink)
	}

	pfNetPath := filepath.Join(nicsBaseDirPath, uplink, "device", "net")
	devices, err := os.ReadDir(pfNetPath)
	if err != nil {
		return "", err
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
		_, vfRepIndex, _ := parsePortName(physPortNameStr)
		// At this point we're confident we have a representor.
		if vfRepIndex == vfIndex {
			return device.Name(), nil
		}
	}
	return "", fmt.Errorf("failed to find VF representor for uplink %s", uplink)
}

func getBrcmPfIndex(sysFsDir string, uplink string) (int, error) {
	pciBaseDirPath := path.Join(sysFsDir, pciPathNameBaseDir)

	ethTool, err := ethtool.NewEthtool()
	if err != nil {
		return 0, fmt.Errorf("new eth tool failed, error: %s", err.Error())
	}

	drvInfo, err := ethTool.DriverInfo(uplink)
	if err != nil {
		return 0, fmt.Errorf("get driver info failed,if %s, error: %s", uplink, err.Error())
	}
	// 获取uplink pci地址，比如0000:3b:00.0，去掉前面0000，得到3b:00.0作为key
	pciAddr := drvInfo.BusInfo
	parts := strings.Split(pciAddr, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("unexpected PCI address format %s", pciAddr)
	}

	pciAddrKey := parts[1] + ":" + parts[2]

	// 拿到uplink的device id，比如1750
	pciPath := filepath.Join(pciBaseDirPath, pciAddr, "device")

	data, err := os.ReadFile(pciPath)
	if err != nil {
		return 0, fmt.Errorf("get device id failed,if %s, error: %s", uplink, err.Error())
	}

	deviceID := strings.TrimPrefix(strings.TrimSpace(string(data)), "0x")

	// lspci | grep 1750，此时两个pf都会被打印出来
	// 在brcm的逻辑下，是按顺序排列的，首列的设备的pf index就是0，随后的pfindex就是1，2，3
	cmd := fmt.Sprintf("lspci | grep %s", deviceID)
	result, err := exec.Command("bash", "-c", cmd).CombinedOutput()

	if err != nil {
		return 0, fmt.Errorf("error running command %s err: %s", cmd, err.Error())
	}

	pfIndex := 0
	lines := strings.Split(string(result), "\n")
	for _, line := range lines {
		if strings.Contains(line, pciAddrKey) {
			return pfIndex, nil
		}

		pfIndex++
	}

	return 0, fmt.Errorf("failed to find PF index for uplink %s, device id %s, lspci output %s", uplink, deviceID, lines)
}
