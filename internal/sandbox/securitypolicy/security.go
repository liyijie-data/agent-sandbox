package securitypolicy

import (
	"fmt"
	"strings"
)

type PodSecurity struct {
	RunAsNonRoot bool

	AllowPrivilegeEscalation bool

	Privileged bool

	DropAllCapabilities bool

	SeccompProfile string

	AutomountServiceAccountToken bool

	HostNetwork, HostPID, HostIPC bool

	HostPathMounts []string

	CPURequest, CPULimit, MemoryRequest, MemoryLimit string
}

func Baseline() PodSecurity {
	return PodSecurity{
		RunAsNonRoot:                 true,
		AllowPrivilegeEscalation:     false,
		Privileged:                   false,
		DropAllCapabilities:          true,
		SeccompProfile:               "RuntimeDefault",
		AutomountServiceAccountToken: false,
		HostNetwork:                  false, HostPID: false, HostIPC: false,
		CPURequest: "250m", CPULimit: "1",
		MemoryRequest: "256Mi", MemoryLimit: "2Gi",
	}
}

func (p PodSecurity) Validate() error {
	if p.Privileged {
		return fmt.Errorf("securitypolicy: privileged containers are forbidden")
	}
	if p.RunAsNonRoot == false {
		return fmt.Errorf("securitypolicy: Runtime must run as non-root")
	}
	if p.AllowPrivilegeEscalation {
		return fmt.Errorf("securitypolicy: privilege escalation must be disabled")
	}
	if !p.DropAllCapabilities {
		return fmt.Errorf("securitypolicy: all capabilities must be dropped")
	}
	if p.SeccompProfile == "" {
		return fmt.Errorf("securitypolicy: seccomp profile is required")
	}
	if p.AutomountServiceAccountToken {
		return fmt.Errorf("securitypolicy: ServiceAccount token auto-mount must be off")
	}
	if p.HostNetwork || p.HostPID || p.HostIPC {
		return fmt.Errorf("securitypolicy: host namespaces are forbidden")
	}
	if len(p.HostPathMounts) > 0 {
		return fmt.Errorf("securitypolicy: hostPath mounts are forbidden")
	}
	if err := validResource(p.CPURequest, "cpu request"); err != nil {
		return err
	}
	if err := validResource(p.CPULimit, "cpu limit"); err != nil {
		return err
	}
	if err := validResource(p.MemoryRequest, "memory request"); err != nil {
		return err
	}
	if err := validResource(p.MemoryLimit, "memory limit"); err != nil {
		return err
	}
	return nil
}

func validResource(v, name string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("securitypolicy: %s must be a finite value", name)
	}
	return nil
}
