package reconciler

import (
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"agent-platform/internal/contracts"
	"agent-platform/internal/security"
)

const NamespaceLabel = "kubernetes.io/metadata.name"

func (r *Runner) renderPolicy(spec contracts.NetworkConfigSpec) (*v1beta1.NetworkPolicySpec, error) {
	ingress := []networkingv1.NetworkPolicyIngressRule{{
		From: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{NamespaceLabel: r.cfg.K8s.Namespace}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": r.cfg.K8s.RegistrationRouterLabel}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: protoPtr(corev1.ProtocolTCP), Port: intstrPtr(8888)}},
	}}

	egress := make([]networkingv1.NetworkPolicyEgressRule, 0, 5+len(spec.NetworkPolicy.Egress))

	egress = append(egress, networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{NamespaceLabel: r.cfg.K8s.Namespace}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"app": r.cfg.K8s.RegistrationServerLabel}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{{Protocol: protoPtr(corev1.ProtocolTCP), Port: intstrPtr(r.cfg.K8s.RegistrationServerPort)}},
	})

	if cidr := r.cfg.K8s.ControlPlaneServiceCIDR; cidr != "" {
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: protoPtr(corev1.ProtocolTCP), Port: intstrPtr(r.cfg.K8s.RegistrationServerPort)}},
		})
	}

	egress = append(egress, networkingv1.NetworkPolicyEgressRule{
		To: []networkingv1.NetworkPolicyPeer{{
			NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{NamespaceLabel: "kube-system"}},
			PodSelector:       &metav1.LabelSelector{MatchLabels: map[string]string{"k8s-app": "kube-dns"}},
		}},
		Ports: []networkingv1.NetworkPolicyPort{
			{Protocol: protoPtr(corev1.ProtocolUDP), Port: intstrPtr(53)},
			{Protocol: protoPtr(corev1.ProtocolTCP), Port: intstrPtr(53)},
		},
	})

	if cidr := r.cfg.K8s.KubeDNSServiceCIDR; cidr != "" {
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
			Ports: []networkingv1.NetworkPolicyPort{
				{Protocol: protoPtr(corev1.ProtocolUDP), Port: intstrPtr(53)},
				{Protocol: protoPtr(corev1.ProtocolTCP), Port: intstrPtr(53)},
			},
		})
	}

	if cidr := r.cfg.K8s.ObjectStorageCIDR; cidr != "" {
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: cidr}}},
			Ports: []networkingv1.NetworkPolicyPort{{Protocol: protoPtr(corev1.ProtocolTCP), Port: intstrPtr(r.cfg.K8s.ObjectStoragePort)}},
		})
	}

	compiled, err := r.baseline.CompileEgress(securityRules(spec.NetworkPolicy.Egress))
	if err != nil {
		return nil, fmt.Errorf("reconciler: compile client egress: %w", err)
	}
	for _, cr := range compiled {
		ip := &networkingv1.IPBlock{CIDR: cr.Allowed.String()}
		for _, ex := range cr.Exclusions {
			ip.Except = append(ip.Except, ex.String())
		}
		egress = append(egress, networkingv1.NetworkPolicyEgressRule{
			To:    []networkingv1.NetworkPolicyPeer{{IPBlock: ip}},
			Ports: toPolicyPorts(cr.Ports),
		})
	}

	return &v1beta1.NetworkPolicySpec{Ingress: ingress, Egress: egress}, nil
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

func toPolicyPorts(ports []security.PortRule) []networkingv1.NetworkPolicyPort {
	sorted := append([]security.PortRule(nil), ports...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Protocol != sorted[j].Protocol {
			return sorted[i].Protocol < sorted[j].Protocol
		}
		return sorted[i].Port < sorted[j].Port
	})
	out := make([]networkingv1.NetworkPolicyPort, 0, len(sorted))
	for _, p := range sorted {
		out = append(out, networkingv1.NetworkPolicyPort{
			Protocol: protoPtr(corev1.Protocol(p.Protocol)),
			Port:     intstrPtr(p.Port),
		})
	}
	return out
}

func protoPtr(p corev1.Protocol) *corev1.Protocol { return &p }

func intstrPtr(p int) *intstr.IntOrString {
	v := intstr.FromInt(p)
	return &v
}
