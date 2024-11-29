package qrm

type NetworkGroup struct {
	NetClassIDs []string `json:"net_class_ids"`
	Egress      uint32   `json:"egress"`
	Ingress     uint32   `json:"ingress"`
	MergedIPv4  string   `json:"merged_ipv4"`
	MergedIPv6  string   `json:"merged_ipv6"`
}
