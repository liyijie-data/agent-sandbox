package reconciler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	sandboxv1beta1 "sigs.k8s.io/agent-sandbox/api/v1beta1"
	extensionsv1beta1 "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"

	"agent-platform/internal/contracts"
)

func nameSuffix(imageRegID, platformRevID string, netRevID *string) string {
	net := ""
	if netRevID != nil {
		net = *netRevID
	}
	sum := sha256.Sum256([]byte(imageRegID + "|" + platformRevID + "|" + net))
	return hex.EncodeToString(sum[:])[:12]
}

func templateName(prefix, imageRegID, platformRevID string, netRevID *string) string {
	return fmt.Sprintf("%s-rt-%s", prefix, nameSuffix(imageRegID, platformRevID, netRevID))
}
func poolName(prefix, imageRegID, platformRevID string, netRevID *string) string {
	return fmt.Sprintf("%s-rp-%s", prefix, nameSuffix(imageRegID, platformRevID, netRevID))
}

func (r *Runner) renderTemplate(name, imageRef string, spec contracts.NetworkConfigSpec) (*extensionsv1beta1.SandboxTemplate, error) {
	pol, err := r.renderPolicy(spec)
	if err != nil {
		return nil, err
	}
	sec := &corev1.PodSecurityContext{
		RunAsNonRoot: boolPtr(true), RunAsUser: int64Ptr(1000), RunAsGroup: int64Ptr(1000), FSGroup: int64Ptr(1000),
	}

	aliasIndex := make(map[string]int, len(spec.HostAliases))
	aliases := make([]corev1.HostAlias, 0, len(spec.HostAliases))
	for _, a := range spec.HostAliases {
		if i, ok := aliasIndex[a.IP]; ok {
			aliases[i].Hostnames = append(aliases[i].Hostnames, a.Hostname)
			continue
		}
		aliasIndex[a.IP] = len(aliases)
		aliases = append(aliases, corev1.HostAlias{IP: a.IP, Hostnames: []string{a.Hostname}})
	}
	volumes := []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	return &extensionsv1beta1.SandboxTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.cfg.K8s.Namespace,
			Labels:    map[string]string{"app": name, "platform.agent-sandbox.io/managed": "reconciler"},
		},
		Spec: extensionsv1beta1.SandboxTemplateSpec{
			SandboxBlueprint: sandboxv1beta1.SandboxBlueprint{
				PodTemplate: sandboxv1beta1.PodTemplate{Spec: corev1.PodSpec{
					SecurityContext: sec,
					HostAliases:     aliases,
					Containers: []corev1.Container{{
						Name:            "python-runtime",
						Image:           imageRef,
						ImagePullPolicy: corev1.PullPolicy(r.cfg.K8s.RegistrationImagePullPolicy),
						Env:             []corev1.EnvVar{{Name: "SANDBOX_EXEC_TIMEOUT_SECONDS", Value: "1800"}},
						Ports:           []corev1.ContainerPort{{ContainerPort: 8888, Name: "sandbox"}},
						ReadinessProbe:  httpProbe("/", 8888, 1),
						LivenessProbe:   httpProbe("/", 8888, 2),
						Resources:       r.sandboxResources(),
						VolumeMounts:    []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}},
					}},
					RestartPolicy:                corev1.RestartPolicyOnFailure,
					AutomountServiceAccountToken: boolPtr(false),
					Volumes:                      volumes,
				}},
			},

			NetworkPolicyManagement: extensionsv1beta1.NetworkPolicyManagementManaged,
			EnvVarsInjectionPolicy:  extensionsv1beta1.EnvVarsInjectionPolicyDisallowed,
			NetworkPolicy:           pol,
		},
	}, nil
}

func (r *Runner) renderPool(name, templateRef string, replicas int32) *extensionsv1beta1.SandboxWarmPool {
	return &extensionsv1beta1.SandboxWarmPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: r.cfg.K8s.Namespace,
			Labels:    map[string]string{"app": name, "platform.agent-sandbox.io/managed": "reconciler"},
		},
		Spec: extensionsv1beta1.SandboxWarmPoolSpec{
			Replicas:    int32Ptr(replicas),
			TemplateRef: extensionsv1beta1.SandboxTemplateRef{Name: templateRef},
		},
	}
}

func (r *Runner) sandboxResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(r.cfg.Security.SandboxCPURequest),
			corev1.ResourceMemory: resource.MustParse(r.cfg.Security.SandboxMemoryRequest),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(r.cfg.Security.SandboxCPULimit),
			corev1.ResourceMemory: resource.MustParse(r.cfg.Security.SandboxMemoryLimit),
		},
	}
}

func httpProbe(path string, port, delay int32) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:  corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: path, Port: intstr.FromInt(int(port))}},
		PeriodSeconds: 1, InitialDelaySeconds: delay,
	}
}

func (r *Runner) ensureTemplate(ctx context.Context, want *extensionsv1beta1.SandboxTemplate) error {
	got, err := r.k8s.SandboxTemplates(r.cfg.K8s.Namespace).Get(ctx, want.Name, metav1.GetOptions{})
	if err == nil {
		if !equality.Semantic.DeepEqual(&got.Spec, &want.Spec) {
			want.ResourceVersion = got.ResourceVersion
			_, uerr := r.k8s.SandboxTemplates(r.cfg.K8s.Namespace).Update(ctx, want, metav1.UpdateOptions{})
			return uerr
		}
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	_, cerr := r.k8s.SandboxTemplates(r.cfg.K8s.Namespace).Create(ctx, want, metav1.CreateOptions{})
	return cerr
}

func (r *Runner) ensurePool(ctx context.Context, want *extensionsv1beta1.SandboxWarmPool) error {
	got, err := r.k8s.SandboxWarmPools(r.cfg.K8s.Namespace).Get(ctx, want.Name, metav1.GetOptions{})
	if err == nil {
		if !equality.Semantic.DeepEqual(&got.Spec, &want.Spec) {
			want.ResourceVersion = got.ResourceVersion
			_, uerr := r.k8s.SandboxWarmPools(r.cfg.K8s.Namespace).Update(ctx, want, metav1.UpdateOptions{})
			return uerr
		}
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	_, cerr := r.k8s.SandboxWarmPools(r.cfg.K8s.Namespace).Create(ctx, want, metav1.CreateOptions{})
	return cerr
}

func (r *Runner) readPoolStatus(ctx context.Context, namespace, name string) (int32, int32, error) {
	pool, err := r.k8s.SandboxWarmPools(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, 0, err
	}
	want := int32(0)
	if pool.Spec.Replicas != nil {
		want = *pool.Spec.Replicas
	}
	return pool.Status.ReadyReplicas, want, nil
}

func (r *Runner) waitPoolReady(ctx context.Context, name string) error {
	deadline := time.Now().Add(r.timeout)
	for {
		ready, want, err := r.poolStatus(ctx, r.cfg.K8s.Namespace, name)
		if err != nil {
			return fmt.Errorf("reconciler: read warm pool %s: %w", name, err)
		}
		if ready >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return &PoolTimeoutError{Name: name, Ready: ready, Want: want, Timeout: r.timeout.String()}
		}
		if err := r.sleep(ctx, r.pollInterval); err != nil {
			return err
		}
	}
}

type PoolTimeoutError struct {
	Name    string
	Ready   int32
	Want    int32
	Timeout string
}

func (e *PoolTimeoutError) Error() string {
	return fmt.Sprintf("reconciler: warm pool %s not ready (%d/%d) within %s", e.Name, e.Ready, e.Want, e.Timeout)
}

func boolPtr(b bool) *bool    { return &b }
func int64Ptr(i int64) *int64 { return &i }
func int32Ptr(i int32) *int32 { return &i }
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
func isNotFound(err error) bool { return err != nil && strings.Contains(err.Error(), "not found") }
