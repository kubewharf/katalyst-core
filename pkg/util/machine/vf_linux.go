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

func GetVfIBDevName(sysFsDir string, vfName string) (ibDevName string, err error) {
	ibDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir, vfName, netFileNameIBDev)

	info, err := os.Stat(ibDirPath)
	if os.IsNotExist(err) || !info.IsDir() {
		return "", fmt.Errorf("device %s does not have an associated InfiniBand/RDMA device", vfName)
	}

	entries, err := os.ReadDir(ibDirPath)
	if err != nil {
		return "", err
	}

	if len(entries) == 0 {
		return "", fmt.Errorf("infiniband directory is empty")
	}

	// 返回第一个找到的目录名
	return entries[0].Name(), nil
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

	err = DoNetNS(nsName, netNSDirAbsPath, func(sysFsDir string) error {
		nicsBaseDirPath := path.Join(sysFsDir, nicPathNAMEBaseDir)
		for _, nic := range nics {
			physfnPath := filepath.Join(nicsBaseDirPath, nic.Name, netFileNamePhysfn)
			// 如果 physfn 不存在，说明它是 PF，跳过
			if _, err = os.Stat(physfnPath); os.IsNotExist(err) {
				continue
			}

			sriovFile := filepath.Join(nicsBaseDirPath, nic.Name, netFileNameNumVFS)
			if _, err = os.Stat(sriovFile); err == nil {
				// 文件不存在，不是 PF
				continue
			}

			vf := VFInterfaceInfo{
				InterfaceInfo: nic,
			}

			vf.PFName, err = GetUplinkRepresentor(sysFsDir, vf.PCIAddr)
			if err != nil {
				continue
			}

			vf.VfID, err = GetVfIndexByPciAddress(sysFsDir, vf.PCIAddr)
			if err != nil {
				continue
			}

			vf.RepName, err = GetVfRepresentor(sysFsDir, vf.PFName, vf.VfID)
			if err != nil {
				continue
			}

			if chechVfTrustOn(vf.PFName, vf.VfID) {
				continue
			}

			vf.CombinedCount, err = GetCombinedChannels(vf.Name)
			if err != nil {
				continue
			}

			// 获取 VF 的 InfiniBand 设备名，这个不是所有vf设备都有，所以这里不检查错误
			vf.IBDevName, _ = GetVfIBDevName(sysFsDir, vf.Name)

			vfs = append(vfs, vf)
		}

		return nil
	})

	return vfs, err
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
