package security

import (
	"fmt"
	"net"
	"strings"
)

func AlwaysReservedCIDRs() []string {
	out := append([]string{}, alwaysSpecialCIDRs...)
	return append(out, "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7")
}

var alwaysSpecialCIDRs = []string{
	"127.0.0.0/8",
	"::1/128",
	"169.254.169.254/32",
	"169.254.0.0/16",
	"fe80::/10",
	"224.0.0.0/4",
	"ff00::/8",
}

type PlatformNetworkBaselineConfig struct {
	PodCIDRs             []string
	ServiceCIDRs         []string
	NodeCIDRs            []string
	ProtectedCIDRs       []string
	BusinessPrivateCIDRs []string
	ReservedHostnames    []string
	IPv6Enabled          bool
}

type PlatformNetworkBaseline struct {
	reserved         *ReservedSet
	platformReserved *ReservedSet
	business         []*net.IPNet
	hostnames        map[string]struct{}
	IPv6Enabled      bool
	allowDNSDeps     bool
}

func NewPlatformNetworkBaseline(cfg PlatformNetworkBaselineConfig) (*PlatformNetworkBaseline, error) {
	platform := append([]string{}, cfg.PodCIDRs...)
	platform = append(platform, cfg.ServiceCIDRs...)
	platform = append(platform, cfg.NodeCIDRs...)
	platform = append(platform, cfg.ProtectedCIDRs...)
	platformOnly := append([]string{}, alwaysSpecialCIDRs...)
	platformOnly = append(platformOnly, platform...)
	pr, err := newReservedSet(platformOnly)
	if err != nil {
		return nil, err
	}
	all, err := NewReservedSet(platform)
	if err != nil {
		return nil, err
	}
	business := make([]*net.IPNet, 0, len(cfg.BusinessPrivateCIDRs))
	for _, raw := range cfg.BusinessPrivateCIDRs {
		n, err := parseCanonicalCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("security: business private cidr: %w", err)
		}
		if !isPrivateNetwork(n) || isULA(n) {
			return nil, fmt.Errorf("security: business private cidr %q is not an IPv4 private zone", raw)
		}
		if pr.Overlaps(n) {
			return nil, fmt.Errorf("security: business private cidr %q overlaps protected platform range", raw)
		}
		business = appendUniqueNetworks(business, n)
	}
	hosts := make(map[string]struct{}, len(cfg.ReservedHostnames))
	for _, raw := range cfg.ReservedHostnames {
		h := strings.ToLower(strings.TrimSpace(raw))
		if h == "" || strings.Contains(h, "*") || strings.ContainsAny(h, " \t/\\") {
			return nil, fmt.Errorf("security: invalid reserved hostname %q", raw)
		}
		hosts[h] = struct{}{}
	}
	return &PlatformNetworkBaseline{reserved: all, platformReserved: pr, business: business, hostnames: hosts, IPv6Enabled: cfg.IPv6Enabled}, nil
}

func (b *PlatformNetworkBaseline) Reserved() *ReservedSet { return b.reserved }

func (b *PlatformNetworkBaseline) ValidateDependencyProtection(endpoints []string) error {
	return b.validateDependencyProtection(endpoints, false)
}

func (b *PlatformNetworkBaseline) ValidateDependencyProtectionAllowDNS(endpoints []string) error {
	return b.validateDependencyProtection(endpoints, true)
}

func (b *PlatformNetworkBaseline) validateDependencyProtection(endpoints []string, allowDNS bool) error {
	for _, raw := range endpoints {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		host, _, err := net.SplitHostPort(s)
		if err != nil {
			host = s
		}
		if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
			if !b.platformReserved.Contains(ip) {
				return fmt.Errorf("security: dependency IP is not protected")
			}
			continue
		}
		if !allowDNS {
			return fmt.Errorf("security: dependency %q must have an explicit protected CIDR", s)
		}
	}
	return nil
}

func (b *PlatformNetworkBaseline) ValidateHostAlias(hostname string, ip net.IP) error {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(hostname), "."))
	if h == "" || strings.Contains(h, "*") || strings.ContainsAny(h, " \t/\\") || strings.HasSuffix(h, ".svc") || strings.HasSuffix(h, ".cluster.local") {
		return fmt.Errorf("security: reserved or invalid host alias %q", hostname)
	}
	if ip == nil || (ip.To4() == nil && !b.IPv6Enabled) {
		return fmt.Errorf("security: invalid or disabled host alias IP")
	}
	if _, ok := b.hostnames[h]; ok || b.platformReserved.Contains(ip) {
		return fmt.Errorf("security: protected host alias %q", hostname)
	}
	if isPrivateIP(ip) {
		allowed := false
		for _, zone := range b.business {
			if zone.Contains(ip) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("security: private host alias outside business zone")
		}
	}
	return nil
}

func (b *PlatformNetworkBaseline) CompileEgress(rules []EgressRule) ([]CompiledRule, error) {
	out := make([]CompiledRule, 0, len(rules))
	for i, rule := range rules {
		if len(rule.Ports) == 0 {
			return nil, fmt.Errorf("security: egress[%d]: no ports", i)
		}
		for _, p := range rule.Ports {
			if !validPort(p) {
				return nil, fmt.Errorf("security: egress[%d]: invalid port", i)
			}
		}
		n, err := parseCanonicalCIDR(rule.CIDR)
		if err != nil {
			return nil, fmt.Errorf("security: egress[%d]: %w", i, err)
		}
		if isIPv6Network(n) && !b.IPv6Enabled {
			return nil, fmt.Errorf("security: IPv6 egress disabled")
		}
		private := isPrivateNetwork(n)
		if private {
			allowed := false
			for _, zone := range b.business {
				if subnetOf(n, zone) {
					allowed = true
					break
				}
			}
			if !allowed || b.platformReserved.Overlaps(n) {
				return nil, fmt.Errorf("security: private egress %q is outside an approved business zone", rule.CIDR)
			}
		}
		ex := b.reserved.ExclusionsFor(n)
		if private {
			ex = b.platformReserved.ExclusionsFor(n)
		}
		out = append(out, CompiledRule{Allowed: n, Ports: append([]PortRule(nil), rule.Ports...), Exclusions: ex})
	}
	return out, nil
}

func parseCanonicalCIDR(raw string) (*net.IPNet, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, fmt.Errorf("empty cidr")
	}
	ip, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil, fmt.Errorf("invalid cidr %q", raw)
	}
	n.IP = ip.Mask(n.Mask)
	return n, nil
}

func validPort(p PortRule) bool {
	return (strings.EqualFold(p.Protocol, "TCP") || strings.EqualFold(p.Protocol, "UDP")) && p.Port >= 1 && p.Port <= 65535
}
func isIPv6Network(n *net.IPNet) bool { return n.IP.To4() == nil }
func isULA(n *net.IPNet) bool {
	return n.Contains(net.ParseIP("fc00::1")) || (isIPv6Network(n) && func() bool { ones, bits := n.Mask.Size(); return ones == 7 && bits == 128 }())
}
func isPrivateNetwork(n *net.IPNet) bool {
	for _, c := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"} {
		_, p, _ := net.ParseCIDR(c)
		if subnetOf(n, p) {
			return true
		}
	}
	return false
}
func isPrivateIP(ip net.IP) bool {
	for _, c := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"} {
		_, p, _ := net.ParseCIDR(c)
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
func appendUniqueNetworks(out []*net.IPNet, n *net.IPNet) []*net.IPNet {
	for _, x := range out {
		if x.String() == n.String() {
			return out
		}
	}
	return append(out, n)
}

type ReservedSet struct {
	nets []*net.IPNet
}

func NewReservedSet(configured []string) (*ReservedSet, error) {
	all := append([]string{}, AlwaysReservedCIDRs()...)
	all = append(all, configured...)
	return newReservedSet(all)
}

func newReservedSet(all []string) (*ReservedSet, error) {
	rs := &ReservedSet{}
	for _, c := range all {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		ipNet, err := parseCanonicalCIDR(c)
		if err != nil {
			return nil, fmt.Errorf("security: reserved cidr %q: %w", c, err)
		}
		duplicate := false
		for _, existing := range rs.nets {
			if existing.String() == ipNet.String() {
				duplicate = true
				break
			}
		}
		if !duplicate {
			rs.nets = append(rs.nets, ipNet)
		}
	}
	return rs, nil
}

func (r *ReservedSet) Contains(ip net.IP) bool {
	for _, n := range r.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func (r *ReservedSet) Overlaps(ipNet *net.IPNet) bool {
	for _, n := range r.nets {
		if netsOverlap(n, ipNet) {
			return true
		}
	}
	return false
}

func (r *ReservedSet) FullyReserved(ipNet *net.IPNet) bool {
	for _, n := range r.nets {
		if subnetOf(ipNet, n) {
			return true
		}
	}
	return false
}

func (r *ReservedSet) ExclusionsFor(ipNet *net.IPNet) []*net.IPNet {
	var out []*net.IPNet
	for _, n := range r.nets {
		if subnetOf(n, ipNet) && netsOverlap(n, ipNet) {
			out = append(out, n)
		}
	}
	return out
}

type EgressRule struct {
	CIDR  string
	Ports []PortRule
}

type PortRule struct {
	Protocol string
	Port     int
}

type CompiledRule struct {
	Allowed    *net.IPNet
	Ports      []PortRule
	Exclusions []*net.IPNet
}

func (r *ReservedSet) CompileEgress(rules []EgressRule) ([]CompiledRule, error) {
	out := make([]CompiledRule, 0, len(rules))
	for i, rule := range rules {
		if len(rule.Ports) == 0 {
			return nil, fmt.Errorf("security: egress[%d]: no ports (no implicit full egress)", i)
		}
		_, ipNet, err := net.ParseCIDR(rule.CIDR)
		if err != nil {
			return nil, fmt.Errorf("security: egress[%d]: invalid cidr %q", i, rule.CIDR)
		}
		if r.FullyReserved(ipNet) {
			return nil, fmt.Errorf("security: egress[%d]: %s is fully reserved", i, rule.CIDR)
		}
		ex := r.ExclusionsFor(ipNet)
		if len(ex) > 0 && !r.Overlaps(ipNet) {

			ex = nil
		}
		out = append(out, CompiledRule{
			Allowed:    ipNet,
			Ports:      rule.Ports,
			Exclusions: ex,
		})
	}
	return out, nil
}

func netsOverlap(a, b *net.IPNet) bool {
	return a.Contains(firstIP(b)) || b.Contains(firstIP(a))
}

func firstIP(n *net.IPNet) net.IP {
	ip := make(net.IP, len(n.IP))
	copy(ip, n.IP)
	return ip
}

func subnetOf(inner, outer *net.IPNet) bool {
	if !outer.Contains(firstIP(inner)) {
		return false
	}
	bitsInner, _ := inner.Mask.Size()
	bitsOuter, _ := outer.Mask.Size()
	return bitsInner >= bitsOuter
}
