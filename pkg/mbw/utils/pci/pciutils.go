// Package pci wraps around the third_party/pciutils C library
// The cgo import is unique to the package. If two go packages both import the
// pci, they cannot pass pointers to pci structures to each other.
// Using interface{} results in panic: interface conversion: interface {} is
// *pci._Ctype_struct_pci_dev
// Therefore, this package serves as the single gateway to the pci cgo.
package pci

/*
#include <stdlib.h>
#include "pci/pci.h"
#include "pci/header.h"

#cgo LDFLAGS: -lpci
*/
import (
	"C"
)
import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	PCI_DEV_FILE_PATH = "/sys/bus/pci/devices"

	IOHC_NB_SMN_INDEX_0_REG = 0x60
	IOHC_NB_SMN_DATA_0_REG  = 0x64
)

// PCIDev exports the pciutils' device struct.
type PCIDev = C.struct_pci_dev

// PCIDevInfo struct is used to export C.struct_pci_dev members.
type PCIDevInfo struct {
	VendorID, DeviceID, Domain uint16
	Bus, Dev, Func             uint8
	HdrType                    int32
}

// GetDevInfo fills a PCIDevInfo from a PCIDev, as PCIDev members are not exported.
func (dev *PCIDev) GetDevInfo() PCIDevInfo {
	info := PCIDevInfo{
		VendorID: uint16(dev.vendor_id),
		DeviceID: uint16(dev.device_id),
		Domain:   uint16(dev.domain_16),
		Bus:      uint8(dev.bus),
		Dev:      uint8(dev.dev),
		Func:     uint8(dev._func),
		HdrType:  int32(dev.hdrtype),
	}

	return info
}

// BDFString gets a device's BDF as a string.
func (dev *PCIDev) BDFString() string {
	return fmt.Sprintf("%04x:%02x:%02x.%d", dev.domain, dev.bus, dev.dev, dev._func)
}

func (dev *PCIDev) GetDevNumaNode() int {
	filePath := fmt.Sprintf("%s/%s/numa_node", PCI_DEV_FILE_PATH, dev.BDFString())
	node, err := general.ReadFileIntoInt(filePath)
	if err != nil {
		general.Errorf("failed to read numa node from the pci dev file %s - vendorID: %x, devID: %x", filePath, dev.vendor_id, dev.device_id)
		return -1
	}

	return node
}

// PCIAccess exports the pciutils' access instance.
var PCIAccess *C.struct_pci_access

// Only one goroutine can call pciutils at a time, because it's not thread-safe.
var m sync.Mutex

// Init allocates an Access instance and initializes it.
func PCIDevInit() {
	m.Lock()
	defer m.Unlock()
	// This code follows pciutils/setpci.c
	PCIAccess = C.pci_alloc() // Get the pci_access structure
	C.pci_init(PCIAccess)     // Initialize the PCI library
}

// Cleanup tears down the Access.
func PCIDevCleanup() {
	m.Lock()
	defer m.Unlock()
	C.pci_cleanup(PCIAccess) // Closes everything at the end.
	general.Infof("close the access to PCI devs")
}

// ScanDevices gets a list of PCI devices representing IOHC
// @param pciVenderID is the pci dev venderID. The one we should pass here is to indicate
// the CPU vendor either AMD or Intel
// @param pciVenderID is the pci dev deivceID. The one we would pass is to specify the CPU
// model (e.g. AMD Genoa)
func ScanDevices(pciVendorID, pciDevID uint16) []*PCIDev {
	m.Lock()
	defer m.Unlock()

	C.pci_scan_bus(PCIAccess)
	devs := PCIAccess.devices
	var devList []*PCIDev
	for dev := devs; dev != nil; dev = dev.next {
		if uint16(dev.vendor_id) == pciVendorID && uint16(dev.device_id) == pciDevID {
			devList = append(devList, dev)
		}
	}

	return devList
}

// GetFirstIOHC gets the first IOHC dev on a socket
// @param node is the first Numa Node ID on the socket
// @param devs is the passed IOHC dev list
func GetFirstIOHC(node int, devs []*PCIDev) *PCIDev {
	var dev *PCIDev
	var minBus uint8 = 0xff

	for i := range devs {
		if devs[i].GetDevNumaNode() == node {
			if uint8(devs[i].bus) < minBus {
				dev = devs[i]
				minBus = uint8(devs[i].bus)
			}
		}
	}

	return dev
}

// WriteLong exports pciutils.pci_write_long().
func WriteLong(dev *PCIDev, addr int32, val uint32) {
	m.Lock()
	defer m.Unlock()
	C.pci_write_long(dev, C.int(addr), C.uint(val))
}

// ReadLong exports pciutils.pci_read_long().
func ReadLong(dev *PCIDev, addr int32) uint32 {
	m.Lock()
	defer m.Unlock()
	return uint32(C.pci_read_long(dev, C.int(addr)))
}

func ReadSMNApp(dev *PCIDev, addr uint32) uint32 {
	WriteLong(dev, IOHC_NB_SMN_INDEX_0_REG, uint32(addr))
	return ReadLong(dev, IOHC_NB_SMN_DATA_0_REG)
}

func WriteSMNApp(dev *PCIDev, addr, data uint32) {
	WriteLong(dev, IOHC_NB_SMN_INDEX_0_REG, uint32(addr))
	WriteLong(dev, IOHC_NB_SMN_DATA_0_REG, data)
}
