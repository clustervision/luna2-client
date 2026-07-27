package model

// Luna captures what we know about the controller side of the install.
type Luna struct {
	Controller  string
	TargetIP    string
	Hostname    string
	Image       string
	InstallMode string
	Target      string
}
