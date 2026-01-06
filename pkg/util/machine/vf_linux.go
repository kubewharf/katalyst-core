package machine

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/safchain/ethtool"
	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/global"
)

type DriverType string

const (
	MellanoxDriverName DriverType = "mlx5_core"
	BroadComDriverName DriverType = "bnxt_en"
	BondDriverName     DriverType = "bonding"
	UnknownDriverName  DriverType = "unknown"
	InvalidDriverName  DriverType = "invalid"
)

func hardwareDriverDetect(ifName string) (DriverType, error) {
	ethTool, err := ethtool.NewEthtool()
	if err != nil {
		klog.Infof("new eth tool failed, error: %s", err.Error())
		return InvalidDriverName, err
	}

	drvInfo, err := ethTool.DriverInfo(ifName)
	if err != nil {
		klog.Infof("get driver info failed,if %s, error: %s", ifName, err.Error())
		return InvalidDriverName, err
	}

	if strings.Contains(drvInfo.Driver, string(MellanoxDriverName)) {
		return MellanoxDriverName, nil
	}

	if strings.Contains(drvInfo.Driver, string(BroadComDriverName)) {
		return BroadComDriverName, nil
	}

	if strings.Contains(drvInfo.Driver, string(BondDriverName)) {
		return BondDriverName, nil
	}

	return UnknownDriverName, nil
}

func chechVfTrustOn(pfName string, vfIndex int) bool {
	link, err := netlink.LinkByName(pfName)
	if err != nil {
		klog.Errorf("failed to lookup netlink link %s, err %s", pfName, err.Error())
		return true
	}

	for _, vf := range link.Attrs().Vfs {
		if vf.ID == vfIndex {
			if vf.Trust == 0 {
				return false
			} else {
				return true
			}
		}
	}

	klog.Infof("vf index %d not found in pf %s", vfIndex, pfName)
	return true
}

func GetVfIBDevName(sysFsDir string, vfName string) (ibDevList []string, err error) {
	ibVerbDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir, vfName, netFileNameIBVerbs)
	ibCMDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir, vfName, netFileNameIBCM)
	ibMadDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir, vfName, netFileNameIBMad)
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
			ibDevList = append(ibDevList, entry.Name())
		}
	}

	return ibDevList, nil
}

func GetCombinedChannels(ifName string) (int, error) {
	// 1. 初始化 Ethtool 句柄
	ethHandle, err := ethtool.NewEthtool()
	if err != nil {
		return 0, fmt.Errorf("ethtool init failed: %v", err)
	}
	defer ethHandle.Close()

	// 2. 发送 ETHTOOL_GCHANNELS 命令
	channels, err := ethHandle.GetChannels(ifName)
	if err != nil {
		return 0, fmt.Errorf("ethtool get channels failed: %v", err)
	}

	// CombinedCount 是当前配置的值 (Current)
	// MaxCombined 是硬件支持的最大值 (Pre-set maximums)
	return int(channels.CombinedCount), nil
}

func GetNSNetworkVFs(nsName, netNSDirAbsPath string) ([]VFInterfaceInfo, error) {
	nics, err := getNSNetworkHardwareTopology(nsName, netNSDirAbsPath)
	if err != nil {
		return nil, err
	}

	var vfs []VFInterfaceInfo
	var pfMap = map[string]InterfaceInfo{}

	err = DoNetNS(nsName, netNSDirAbsPath, func(sysFsDir string) error {
		nicsBaseDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir)
		for _, nic := range nics {
			physfnPath := filepath.Join(nicsBaseDirPath, nic.Name, netFileNamePhysfn)
			// physfnPath不存在且sriovPath存在，说明这是 PF
			if _, err = os.Stat(physfnPath); err == nil {
				klog.Infof("physfn %s already exists", physfnPath)
				continue
			}

			sriovPath := filepath.Join(nicsBaseDirPath, nic.Name, netFileNameNumVFS)
			if _, err = os.Stat(sriovPath); err != nil {
				klog.Infof("sriov %s not exists", sriovPath)
				continue
			}

			pfMap[nic.Name] = nic
			klog.Infof("pf nic %s detected", nic.Name)
		}

		for _, pf := range pfMap {
			vfMap, err := GetVfMapFromPF(sysFsDir, pf.PCIAddr)
			if err != nil {
				klog.Errorf("get vf map from pf %s failed, error: %s", pf.Name, err.Error())
				continue
			}

			for vfIndex, vfPci := range vfMap {
				vf := VFInterfaceInfo{
					PCIAddr: vfPci,
					PFInfo:  pf,
					VfID:    vfIndex,
				}

				vf.RepName, err = GetVfRepresentor(sysFsDir, pf.Name, vf.VfID)
				if err != nil {
					klog.Errorf("get vf %d representor failed of %s, error: %s", pf.Name, err.Error())
					continue
				}

				if chechVfTrustOn(pf.Name, vf.VfID) {
					continue
				}

				vfs = append(vfs, vf)
				klog.Infof("vf %s detected, pf %s", vfPci, pf.Name)
			}
		}

		return nil
	})

	return vfs, err
}
func GetNSNetworkVFName(sysFsDir string, vfPciAddress string) (string, error) {
	pciBaseDirPath := path.Join(sysFsDir, pciPathNameBaseDir)
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

func getNSNetworkPFs(nsName, netNSDirAbsPath string) ([]InterfaceInfo, error) {
	var pfs []InterfaceInfo

	nics, err := getNSNetworkHardwareTopology(nsName, netNSDirAbsPath)
	if err != nil {
		return nil, err
	}

	err = DoNetNS(nsName, netNSDirAbsPath, func(sysFsDir string) error {
		nicsBaseDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir)
		for _, nic := range nics {
			physfnPath := filepath.Join(nicsBaseDirPath, nic.Name, netFileNamePhysfn)
			if _, err = os.Stat(physfnPath); err == nil {
				// 文件存在，说明这是 VF
				continue
			}

			sriovFile := filepath.Join(nicsBaseDirPath, nic.Name, netFileNameNumVFS)
			if _, err = os.Stat(sriovFile); err == nil {
				// 文件不存在，不是 PF
				continue
			}

			pfs = append(pfs, nic)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return pfs, nil
}

func GetNetworkVFs(conf *global.MachineInfoConfiguration) ([]VFInterfaceInfo, error) {
	nsList := []string{DefaultNICNamespace}

	nsList = append(nsList, conf.NetAllocatableNS...)
	if conf.NetNSDirAbsPath == "" {
		return nil, fmt.Errorf("GetNetworkVFs got nil netNSDirAbsPath")
	}
	vfNics := []VFInterfaceInfo{}
	for _, ns := range nsList {
		vfs, err := GetNSNetworkVFs(ns, conf.NetNSDirAbsPath)
		if err != nil {
			klog.Errorf("GetNSNetworkVFs %s failed, error: %s", ns, err.Error())
			continue
		}

		vfNics = append(vfNics, vfs...)
	}

	return vfNics, nil
}
