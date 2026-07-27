package model

// Node holds hardware facts about the install target.
type Node struct {
	Name     string
	BootMode string
	Arch     string
	Vendor   string
	Model    string
	// CPU is the model name (e.g. "Intel Xeon Gold 5218"); CPUTopo is the
	// compact topology summary (e.g. "4 vCPU · 4S/1C/1T") gathered from
	// /proc/cpuinfo (no lscpu dependency - it isn't in the install env).
	CPU     string
	CPUTopo string
	Memory  string
	NICs    string
	IB      string
	// GPU is a one-line accelerator summary ("2 × NVIDIA", "none", …)
	// derived from PCI class/vendor in /sys (no lspci dependency).
	GPU       string
	GPUNvidia string
	GPUAMD    string
}
