package outbound

type EasyTierWebClientOption struct {
	Endpoint   string `proxy:"endpoint"`
	MachineID  string `proxy:"machine-id,omitempty"`
	Hostname   string `proxy:"hostname,omitempty"`
	SecureMode bool   `proxy:"secure-mode,omitempty"`
}
