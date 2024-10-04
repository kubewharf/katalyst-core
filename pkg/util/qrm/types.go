package qrm

type NetworkGroup struct {
	NetClassIDs []string `json:"net_class_ids"`
	Egress      uint32   `json:"egress"`
	Ingress     uint32   `json:"ingress"`
}
