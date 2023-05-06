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

package staticpolicy

import (
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/vishvananda/netns"
	"k8s.io/apimachinery/pkg/util/sets"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
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

type NICFilter func(nics []*NetworkInterface, req *pluginapi.ResourceRequest, agentCtx *agent.GenericContext) []*NetworkInterface

var nicFilters = []NICFilter{
	//[TODO]: inject dns && consul check
	filterNICsByAvailability,
	filterNICsByNamespaceType,
	filterNICsByHint,
}

func isReqAffinityRestricted(reqAnnotations map[string]string) bool {
	return reqAnnotations[consts.PodAnnotationNetworkEnhancementAffinityRestricted] ==
		consts.PodAnnotationNetworkEnhancementAffinityRestrictedTrue
}

func isReqNamespaceRestricted(reqAnnotations map[string]string) bool {
	return reqAnnotations[consts.PodAnnotationNetworkEnhancementNamespaceType] ==
		consts.PodAnnotationNetworkEnhancementNamespaceTypeHost ||
		reqAnnotations[consts.PodAnnotationNetworkEnhancementNamespaceType] ==
			consts.PodAnnotationNetworkEnhancementNamespaceTypeNotHost
}

func getNICPreferenceOfReq(nic *NetworkInterface, reqAnnotations map[string]string) (bool, error) {
	if nic == nil {
		return false, fmt.Errorf("getNICPreferenceOfReq got nil nic")
	}

	switch reqAnnotations[consts.PodAnnotationNetworkEnhancementNamespaceType] {
	case consts.PodAnnotationNetworkEnhancementNamespaceTypeHost:
		if nic.NSName == "" {
			return true, nil
		} else {
			return false, fmt.Errorf("getNICPreferenceOfReq got invalid nic: %s with %s: %s, NSName: %s",
				nic.Name, consts.PodAnnotationNetworkEnhancementNamespaceType,
				consts.PodAnnotationNetworkEnhancementNamespaceTypeHost, nic.NSName)
		}
	case consts.PodAnnotationNetworkEnhancementNamespaceTypeHostPrefer:
		if nic.NSName == "" {
			return true, nil
		} else {
			return false, nil
		}
	case consts.PodAnnotationNetworkEnhancementNamespaceTypeNotHost:
		if nic.NSName != "" {
			return true, nil
		} else {
			return false, fmt.Errorf("getNICPreferenceOfReq got invalid nic: %s with %s: %s, NSName: %s",
				nic.Name, consts.PodAnnotationNetworkEnhancementNamespaceType,
				consts.PodAnnotationNetworkEnhancementNamespaceTypeHost, nic.NSName)
		}
	case consts.PodAnnotationNetworkEnhancementNamespaceTypeNotHostPrefer:
		if nic.NSName != "" {
			return true, nil
		} else {
			return false, nil
		}
	default:
		return false, nil
	}
}

func filterNICsByAvailability(nics []*NetworkInterface, req *pluginapi.ResourceRequest, _ *agent.GenericContext) []*NetworkInterface {

	filteredNICs := make([]*NetworkInterface, 0, len(nics))

	for _, nic := range nics {
		if nic == nil {
			general.Warningf("found nil nic")
			continue
		} else if !nic.Enabled {
			general.Warningf("nic: %s isn't enabled", nic.Name)
			continue
		} else if len(nic.Addr.IPv4) == 0 && len(nic.Addr.IPv6) == 0 {
			general.Warningf("nic: %s doesn't have IP address", nic.Name)
			continue
		}

		filteredNICs = append(filteredNICs, nic)
	}

	return filteredNICs
}

func filterNICsByNamespaceType(nics []*NetworkInterface, req *pluginapi.ResourceRequest, _ *agent.GenericContext) []*NetworkInterface {
	filteredNICs := make([]*NetworkInterface, 0, len(nics))

	for _, nic := range nics {
		if nic == nil {
			general.Warningf("found nil nic")
			continue
		}

		filterOut := true
		switch req.Annotations[consts.PodAnnotationNetworkEnhancementNamespaceType] {
		case consts.PodAnnotationNetworkEnhancementNamespaceTypeHost:
			if nic.NSName == "" {
				filteredNICs = append(filteredNICs, nic)
				filterOut = false
			}
		case consts.PodAnnotationNetworkEnhancementNamespaceTypeNotHost:
			if nic.NSName != "" {
				filteredNICs = append(filteredNICs, nic)
				filterOut = false
			}
		default:
			filteredNICs = append(filteredNICs, nic)
			filterOut = false
		}

		if filterOut {
			general.Infof("filter out nic: %s mismatching with enhancement %s: %s",
				nic.Name, consts.PodAnnotationNetworkEnhancementNamespaceType, consts.PodAnnotationNetworkEnhancementNamespaceTypeHost)
		}
	}

	return filteredNICs
}

func filterNICsByHint(nics []*NetworkInterface, req *pluginapi.ResourceRequest, agentCtx *agent.GenericContext) []*NetworkInterface {
	// means not to filter by hint (in topology hint calculation period)
	if req.Hint == nil {
		general.InfoS("req hint is nil, skip filterNICsByHint",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName)
		return nics
	}

	var exactlyMatchNIC *NetworkInterface
	hintMatchedNICs := make([]*NetworkInterface, 0, len(nics))

	hintNUMASet, err := machine.NewCPUSetUint64(req.Hint.Nodes...)

	if err != nil {
		general.Errorf("NewCPUSetUint64 failed with error: %v, filter out all nics", err)
		return nil
	}

	for _, nic := range nics {
		if nic == nil {
			general.Warningf("found nil nic")
			continue
		}

		siblingNUMAs, err := getSiblingNUMAs(nic.AffinitiveNUMANode, agentCtx.CPUTopology)

		if err != nil {
			general.Errorf("get siblingNUMAs for nic: %s failed with error: %v, filter out it",
				nic.Name, err)
			continue
		}

		if siblingNUMAs.Equals(hintNUMASet) {
			// [TODO]: if multi-nics meets the hint, we need to choose best one according to left bandwidth or other properties
			if exactlyMatchNIC == nil {
				general.InfoS("add hint exactly matched nic",
					"podNamespace", req.PodNamespace,
					"podName", req.PodName,
					"containerName", req.ContainerName,
					"nic", nic.Name,
					"siblingNUMAs", siblingNUMAs.String(),
					"hintNUMASet", hintNUMASet.String())
				exactlyMatchNIC = nic
			}
		} else if siblingNUMAs.IsSubsetOf(hintNUMASet) { // for pod affinity_restricted != true
			general.InfoS("add hint matched nic",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"nic", nic.Name,
				"siblingNUMAs", siblingNUMAs.String(),
				"hintNUMASet", hintNUMASet.String())
			hintMatchedNICs = append(hintMatchedNICs, nic)
		}
	}

	if exactlyMatchNIC != nil {
		return []*NetworkInterface{exactlyMatchNIC}
	} else {
		return hintMatchedNICs
	}
}

func GetNetworkInterfaces(netNSDirAbsPath string) ([]*NetworkInterface, error) {
	if netNSDirAbsPath == "" {
		return nil, fmt.Errorf("GetNetworkInterfaces got nil netNSDirAbsPath")
	}

	var nics []*NetworkInterface

	nsList := []string{""}
	if dirs, err := ioutil.ReadDir(netNSDirAbsPath); err != nil {
		return nil, err
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
		nicsInNs, err := getNSNetworkHardwareTopology(ns, netNSDirAbsPath)
		if err != nil {
			// [TODO]: to discuss if block plugin start when any ns is parsed failed
			general.Errorf("get network topology for ns %s failed: %v", ns, err)
			continue
		}

		nics = append(nics, nicsInNs...)
	}
	return nics, nil
}

func getRandomNICs(nics []*NetworkInterface) *NetworkInterface {
	rand.Seed(time.Now().UnixNano())
	return nics[rand.Intn(len(nics))]
}

func getNICIPs(nic *NetworkInterface, ipVersion int) []string {
	res := sets.NewString()

	if nic == nil {
		return []string{}
	}

	var targetIPs []net.IP
	switch ipVersion {
	case 4:
		targetIPs = nic.Addr.IPv4
	case 6:
		targetIPs = nic.Addr.IPv6
	}

	for _, ip := range targetIPs {
		res.Insert(ip.String())
	}

	return res.List()
}

func getNetworkInterfaceAddr() (map[string]NetworkInterfaceAddr, error) {
	nicsAddrMaps := make(map[string]NetworkInterfaceAddr)

	interfaceList, err := net.Interfaces()
	if err != nil {
		return nicsAddrMaps, err
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
					ia.IPv4 = append(ia.IPv4, ip)
				} else {
					ia.IPv6 = append(ia.IPv6, ip)
				}
			}
			nicsAddrMaps[i.Name] = ia
		}
	}
	return nicsAddrMaps, nil
}

// getNSNetworkHardwareTopology set given network namespaces and get nics inside if needed
func getNSNetworkHardwareTopology(nsName, netNSDirAbsPath string) ([]*NetworkInterface, error) {
	var nics []*NetworkInterface

	sysFsDir := normalSysFsDir
	nsAbsPath := ""

	if nsName != "" {
		sysFsDir = tmpSysFsDirInNetNS
		nsAbsPath = path.Join(netNSDirAbsPath, nsName)

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
			return nil, fmt.Errorf("get handle from net ns path: %s failed with error: %v", nsAbsPath, err)
		}

		defer func() {
			_ = newNS.Close()
		}()

		if err = netns.Set(newNS); err != nil {
			return nil, fmt.Errorf("set newNS: %s failed with error: %v", nsAbsPath, err)
		}

		// create the target directory if it doesn't exist
		if _, err := os.Stat(tmpSysFsDirInNetNS); err != nil {
			if os.IsNotExist(err) {
				if err := os.MkdirAll(tmpSysFsDirInNetNS, os.FileMode(0755)); err != nil {
					return nil, fmt.Errorf("make dir: %s failed with error: %v",
						tmpSysFsDirInNetNS, err)
				}
			} else {
				return nil, fmt.Errorf("check dir: %s failed with error: %v",
					tmpSysFsDirInNetNS, err)
			}
		}

		if err := syscall.Mount("sysfs", tmpSysFsDirInNetNS, "sysfs", 0, ""); err != nil {
			return nil, fmt.Errorf("mount sysfs to %s failed with error: %v", tmpSysFsDirInNetNS, err)
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
		return nil, err
	}

	nicsAddrMap, err := getNetworkInterfaceAddr()
	if err != nil {
		return nil, err
	}

	for _, nicDir := range nicDirs {
		nicName := nicDir.Name()

		nicPath := path.Join(nicsBaseDirPath, nicName)

		devPath, err := filepath.EvalSymlinks(nicPath)
		if err != nil {
			general.Warningf("eval sym link: %s failed with error: %v", nicPath, err)
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
			return nil, fmt.Errorf("read affinitive NUMA node for nic: %s failed with error: %v", nicName, err)
		}

		nicEnableFilePath := path.Join(nicPath, nicEnableFileSuffix)
		nicEnabledStatus, err := general.ReadFileIntoInt(nicEnableFilePath)

		if err != nil {
			return nil, fmt.Errorf("read enable status for nic: %s failed with error: %v", nicName, err)
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

		general.Infof("discover nic: %#v", nic)

		nics = append(nics, nic)
	}

	return nics, nil
}

func filterAvailableNICsByReq(nics []*NetworkInterface, req *pluginapi.ResourceRequest, agentCtx *agent.GenericContext) ([]*NetworkInterface, error) {

	if req == nil {
		return nil, fmt.Errorf("filterAvailableNICsByReq got nil req")
	} else if agentCtx == nil {
		return nil, fmt.Errorf("filterAvailableNICsByReq got nil agentCtx")
	}

	filteredNICs := nics
	for _, nicFilter := range nicFilters {
		filteredNICs = nicFilter(filteredNICs, req, agentCtx)
	}

	return filteredNICs, nil
}

func getSiblingNUMAs(numaId int, topology *machine.CPUTopology) (machine.CPUSet, error) {
	if topology == nil {
		return machine.NewCPUSet(), fmt.Errorf("getSiblingNUMAs got nil topology")
	}

	socketSet := topology.CPUDetails.SocketsInNUMANodes(numaId)

	if socketSet.Size() != 1 {
		return machine.NewCPUSet(), fmt.Errorf("get invalid socketSet: %s from NUMA: %d",
			socketSet.String(), numaId)
	}

	numaSet := topology.CPUDetails.NUMANodesInSockets(socketSet.ToSliceNoSortInt()...)

	if numaSet.IsEmpty() {
		return machine.NewCPUSet(), fmt.Errorf("get empty numaSet from socketSet: %s",
			socketSet.String())
	}

	return numaSet, nil
}
