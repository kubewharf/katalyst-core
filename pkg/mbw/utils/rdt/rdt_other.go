//go:build !(amd64 && !gccgo && !noasm && !appengine)
// +build !amd64 gccgo noasm appengine

package rdt

const (
	PQR_ASSOC int64 = 0xC8F

	PQOS_MON_EVENT_TMEM_BW PQOS_EVENT_TYPE = 2
	PQOS_MON_EVENT_LMEM_BW PQOS_EVENT_TYPE = 3
)

type PQOS_EVENT_TYPE uint64

func GetRDTScalar() uint32 {
	panic("not available")
}

func GetRDTValue(core uint32, event PQOS_EVENT_TYPE) (uint64, error) {
	panic("not available")
}
