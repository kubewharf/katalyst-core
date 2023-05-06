package dynamicpolicy

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/vishvananda/netns"
)

const (
	normalSysFsDir         = "/sys"
	tmpSysFsDirInNetNS     = "/tmp/net_ns_sysfs"
	nicsBaseDirSuffix      = "class/net"
	pciDevicesDirFormatStr = "devices/pci"
	nicNUMANodeFileSuffix  = "device/numa_node"
	nicEnableFileSuffix    = "device/enable"
	nicEnabled             = 1
)

func GetNetworkInterfaces(netNSDirAbsPath string) ([]*NetworkInterface, error) {
	var result []*NetworkInterface

	nsList := []string{""}
	if dirs, err := ioutil.ReadDir(netNSDirAbsPath); err != nil {
		return result, err
	} else {
		for _, dir := range dirs {
			if !dir.IsDir() {
				nsList = append(nsList, dir.Name())
			}
		}
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	for _, ns := range nsList {
		nicsInNs, err := getNSNetworkHardwareTopology(ns)
		if err != nil {
			general.Errorf("get network topology for ns %s failed: %v", ns, err)
			continue
		}

		result = append(result, nicsInNs...)
	}
	return result, nil
}

// actually, this is not a truly random function, host ns will always prefer than other ns
func GetRandomNetwork(nList []Network) Network {
	for _, n := range nList {
		if len(n.AbsoluteNS) == 0 {
			return n
		}
	}

	rand.Seed(time.Now().UnixNano())
	return nList[rand.Intn(len(nList))]
}

func MergeNetworkIPV4(nList []Network) []string {
	res := sets.NewString()
	for _, n := range nList {
		for _, ip := range n.Addr.IPV4 {
			res.Insert(ip.String())
		}
	}
	return res.List()
}

func MergeNetworkIPV6(nList []Network) []string {
	res := sets.NewString()
	for _, n := range nList {
		for _, ip := range n.Addr.IPV6 {
			res.Insert(ip.String())
		}
	}
	return res.List()
}

func simpleReadInt(file string) int {
	body, err := ioutil.ReadFile(file)
	if err != nil {
		return -1
	}

	i, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		return -2
	}

	return i
}

func getNetworkInterfaceAddr() (map[string]NetworkInterfaceAddr, error) {
	results := make(map[string]NetworkInterfaceAddr)

	interfaceList, err := net.Interfaces()
	if err != nil {
		return results, err
	}

	for _, i := range interfaceList {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback > 0 {
			continue
		}

		addrList, err := i.Addrs()
		if err != nil {
			continue
		}

		if len(addrList) > 0 {
			ia := NetworkInterfaceAddr{}
			for _, addr := range addrList {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				default:
					continue
				}

				// filter out ips that are not global uni-cast
				if !ip.IsGlobalUnicast() {
					continue
				}

				if ip.To4() != nil {
					ia.IPV4 = append(ia.IPV4, ip)
				} else {
					ia.IPV6 = append(ia.IPV6, ip)
				}
			}
			results[i.Name] = ia
		}
	}
	return results, nil
}

// for give network namespace, exec and get network nic if needed
func getNSNetworkHardwareTopology(nsName, netNSDirAbsPath string) ([]*NetworkInterface, error) {
	var result []*NetworkInterface

	sysFsDir := normalSysFsDir
	nsAbsPath := path.Join(netNSDirAbsPath, nsName)

	if nsName != "" {
		sysFsDir = tmpSysFsDirInNetNS

		// save the current network namespace
		originNS, _ := netns.Get()
		defer func() {
			// switch back to the original namespace
			if err := netns.Set(originNS); err != nil {
				general.Fatalf("failed to unmount sys fs: %v", err)
			}
			_ = originNS.Close()
		}()

		// exec into the new network namespace
		newNS, err := netns.GetFromPath(nsAbsPath)

		if err != nil {
			return result, fmt.Errorf("get handle from net ns path: %s failed with error: %v", nsAbsPath, err)
		}

		defer func() {
			_ = newNS.Close()
		}()

		if err = netns.Set(newNS); err != nil {
			return result, fmt.Errorf("set newNS: %s failed with error: %v", nsAbsPath, err)
		}

		// create the target directory if it doesn't exist
		if _, err := os.Stat(tmpSysFsDirInNetNS); err != nil {
			if os.IsNotExist(err) {
				if err := os.MkdirAll(tmpSysFsDirInNetNS, os.FileMode(0755)); err != nil {
					return result, fmt.Errorf("make dir: %s failed with error: %v",
						tmpSysFsDirInNetNS, err)
				}
			} else {
				return result, fmt.Errorf("check dir: %s failed with error: %v",
					tmpSysFsDirInNetNS, err)
			}
		}

		if err := syscall.Mount("sysfs", tmpSysFsDirInNetNS, "sysfs", 0, ""); err != nil {
			return result, fmt.Errorf("mount sysfs to %s failed with error: %v", tmpSysFsDirInNetNS, err)
		}

		// the sysfs needs to be remounted before switching network namespace back
		defer func() {
			if err := syscall.Unmount(tmpSysFsDirInNetNS, 0); err != nil {
				general.Fatalf("unmount sysfs: %s failed with error: %v", tmpSysFsDirInNetNS, err)
			}
		}()
	}

	nicsBaseDirPath := path.Join(sysFsDir, nicsBaseDirSuffix)
	nicDirs, err := ioutil.ReadDir(nicsBaseDirPath)
	if err != nil {
		return result, err
	}

	nicsAddrMap, err := getNetworkInterfaceAddr()
	if err != nil {
		return result, err
	}

	for _, nicDir := range nicDirs {
		nicName := nicDir.Name()

		nicPath := path.Join(nicsBaseDirPath, nicName)

		devPath, err := filepath.EvalSymlinks(nicPath)
		if err != nil {
			general.Warningf("eval sym link: %s failed with error: %v", nicPath)
			continue
		}

		// only return PCI NIC
		if !strings.Contains(devPath, pciDevicesDirFormatStr) {
			general.Warningf("skip nic: %s with devPath: %s which isn't pci device", nicName, devPath)
			continue
		}

		nicNUMANodeFilePath := path.Join(nicPath, nicNUMANodeFileSuffix)
		affinitiveNUMANode, err := general.ReadFileIntoInt(nicNUMANodeFilePath)
		if err != nil {
			return result, fmt.Errorf("read affinitive NUMA node for nic: %s failed with error: %v", nicName, err)
		}

		nicEnableFilePath := path.Join(nicPath, nicEnableFileSuffix)
		nicEnabledStatus, err := general.ReadFileIntoInt(nicEnableFilePath)

		if err != nil {
			return result, fmt.Errorf("read enable status for nic: %s failed with error: %v", nicName, err)
		}

		nic := &NetworkInterface{
			Name:               nicName,
			AffinitiveNUMANode: affinitiveNUMANode,
			Enabled:            nicEnabledStatus == nicEnabled,
			NSName:             nsName,
			NSAbsolutePath:     nsAbsPath,
		}

		if nicAddr, exist := nicsAddrMap[nicName]; exist {
			nic.Addr = nicAddr
		}

		result = append(result, nic)
	}

	return result, nil
}
