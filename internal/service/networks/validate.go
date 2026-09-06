package networks

import (
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"

	"agent-platform/internal/contracts"
	"agent-platform/internal/security"
)

func (s *Service) canonicalConfig(in contracts.NetworkConfigRequest) ([]byte, []byte, error) {
	aliases, err := s.canonicalAliases(in.HostAliases, in.NetworkPolicy.Egress)
	if err != nil {
		return nil, nil, err
	}
	egress, err := s.canonicalEgress(in.NetworkPolicy.Egress)
	if err != nil {
		return nil, nil, err
	}
	spec := contracts.NetworkConfigSpec{HostAliases: aliases, NetworkPolicy: contracts.NetworkPolicy{Egress: egress}}
	cfgJSON, err := jsonMarshal(spec)
	if err != nil {
		return nil, nil, err
	}
	return cfgJSON, hashOf(cfgJSON), nil
}

func (s *Service) canonicalAliases(hosts []contracts.HostAlias, egress []contracts.EgressRule) ([]contracts.HostAlias, error) {
	compiled, err := s.baseline.CompileEgress(securityRules(egress))
	if err != nil {
		return nil, s.rejected("egress rejected by platform baseline: %v", err)
	}
	seen := map[string]bool{}
	out := make([]contracts.HostAlias, 0, len(hosts))
	for _, a := range hosts {
		ip := net.ParseIP(strings.TrimSpace(a.IP))
		if ip == nil {
			return nil, s.rejected("invalid host alias ip %q", a.IP)
		}
		hostname := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(a.Hostname), "."))
		if err := s.baseline.ValidateHostAlias(hostname, ip); err != nil {
			return nil, s.rejected("host alias %q rejected: %v", hostname, err)
		}
		if seen[hostname] {
			continue
		}
		if !aliasCovered(compiled, ip) {
			return nil, s.rejected("host alias %q ip %s is not covered by any egress rule", hostname, ip.String())
		}
		seen[hostname] = true
		out = append(out, contracts.HostAlias{Hostname: hostname, IP: ip.String()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out, nil
}

func (s *Service) canonicalEgress(rules []contracts.EgressRule) ([]contracts.EgressRule, error) {
	out := make([]contracts.EgressRule, 0, len(rules))
	for i, r := range rules {
		_, n, err := net.ParseCIDR(strings.TrimSpace(r.CIDR))
		if err != nil {
			return nil, s.rejected("egress[%d]: invalid cidr %q", i, r.CIDR)
		}
		if !n.IP.IsPrivate() && s.baseline.Reserved().FullyReserved(n) {
			return nil, s.rejected("egress[%d]: %s is a platform protected target", i, r.CIDR)
		}
		ports := append([]contracts.PortRule(nil), r.Ports...)
		sort.Slice(ports, func(x, y int) bool {
			if ports[x].Protocol != ports[y].Protocol {
				return ports[x].Protocol < ports[y].Protocol
			}
			return ports[x].Port < ports[y].Port
		})
		out = append(out, contracts.EgressRule{CIDR: n.String(), Ports: ports})
	}
	sort.Slice(out, func(x, y int) bool { return out[x].CIDR < out[y].CIDR })
	if _, err := s.baseline.CompileEgress(securityRules(out)); err != nil {
		return nil, s.rejected("egress rejected by platform baseline: %v", err)
	}
	return out, nil
}

func aliasCovered(compiled []security.CompiledRule, ip net.IP) bool {
	for _, cr := range compiled {
		if !cr.Allowed.Contains(ip) {
			continue
		}
		excluded := false
		for _, ex := range cr.Exclusions {
			if ex.Contains(ip) {
				excluded = true
				break
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}

func securityRules(rules []contracts.EgressRule) []security.EgressRule {
	out := make([]security.EgressRule, 0, len(rules))
	for _, r := range rules {
		ports := make([]security.PortRule, 0, len(r.Ports))
		for _, p := range r.Ports {
			ports = append(ports, security.PortRule{Protocol: p.Protocol, Port: p.Port})
		}
		out = append(out, security.EgressRule{CIDR: r.CIDR, Ports: ports})
	}
	return out
}

func jsonMarshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("networks: canonical config marshal: %w", err)
	}
	return b, nil
}

func (s *Service) rejected(format string, args ...any) error {
	s.log.Warn("networks: configuration rejected", "reason", fmt.Sprintf(format, args...))
	return fmt.Errorf("%w: %s", ErrNetworkRejected, fmt.Sprintf(format, args...))
}
