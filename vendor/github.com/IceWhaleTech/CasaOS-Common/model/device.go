package model

type DeviceInfo struct {
	LanIpv4     []string            `json:"lan_ipv4"`
	Port        int                 `json:"port"`
	DeviceName  string              `json:"device_name"`
	DeviceModel string              `json:"device_model"`
	Initialized bool                `json:"initialized"`
	OS_Version  string              `json:"os_version"`
	Hash        string              `json:"hash"`
	RequestIp   string              `json:"request_ip,omitempty"`
	TB_Ipv4     []string            `json:"tb_ipv4"`
	Ip4         []map[string]string `json:"ip4"`
}
