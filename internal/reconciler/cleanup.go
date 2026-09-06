package reconciler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agent-platform/internal/cleanup"
	"agent-platform/model"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	extensionsv1beta1 "sigs.k8s.io/agent-sandbox/clients/k8s/extensions/clientset/versioned/typed/api/v1beta1"
)

type K8sCleanupDeleter struct {
	Namespace string
	Ext       extensionsv1beta1.ExtensionsV1beta1Interface
	Pods      corev1.CoreV1Interface
}

func (d *K8sCleanupDeleter) DeleteK8sObject(ctx context.Context, kind, name string) (cleanup.DeletionOutcome, error) {
	switch kind {
	case cleanup.ResourceSandboxTemplate:
		if _, err := d.Ext.SandboxTemplates(d.Namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if isNotFound(err) {
				return cleanup.OutcomeAlreadyGone, nil
			}
			return 0, err
		}
		if err := d.Ext.SandboxTemplates(d.Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return 0, err
		}
		if _, err := d.Ext.SandboxTemplates(d.Namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if isNotFound(err) {
				return cleanup.OutcomeDeleted, nil
			}
			return 0, err
		}
		return 0, fmt.Errorf("cleanup: template %q still exists after deletion", name)
	case cleanup.ResourceSandboxWarmPool:
		if _, err := d.Ext.SandboxWarmPools(d.Namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if isNotFound(err) {
				return cleanup.OutcomeAlreadyGone, nil
			}
			return 0, err
		}
		if err := d.Ext.SandboxWarmPools(d.Namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
			return 0, err
		}
		if _, err := d.Ext.SandboxWarmPools(d.Namespace).Get(ctx, name, metav1.GetOptions{}); err != nil {
			if isNotFound(err) {
				return cleanup.OutcomeDeleted, nil
			}
			return 0, err
		}
		return 0, fmt.Errorf("cleanup: warm pool %q still exists after deletion", name)
	default:
		return 0, fmt.Errorf("cleanup: unsupported k8s resource kind %q", kind)
	}
}

func (d *K8sCleanupDeleter) DeletePod(ctx context.Context, podName string) (cleanup.DeletionOutcome, error) {
	if d.Pods == nil {
		return 0, fmt.Errorf("cleanup: no corev1 client wired for pod deletion")
	}
	if _, err := d.Pods.Pods(d.Namespace).Get(ctx, podName, metav1.GetOptions{}); err != nil {
		if isNotFound(err) {
			return cleanup.OutcomeAlreadyGone, nil
		}
		return 0, err
	}
	if err := d.Pods.Pods(d.Namespace).Delete(ctx, podName, metav1.DeleteOptions{}); err != nil {
		return 0, err
	}
	if _, err := d.Pods.Pods(d.Namespace).Get(ctx, podName, metav1.GetOptions{}); err != nil {
		if isNotFound(err) {
			return cleanup.OutcomeDeleted, nil
		}
		return 0, err
	}
	return 0, fmt.Errorf("cleanup: pod %q still exists after deletion", podName)
}

var _ cleanup.K8sResourceDeleter = (*K8sCleanupDeleter)(nil)
var _ cleanup.PodDeleter = (*K8sCleanupDeleter)(nil)

func (r *Runner) CleanupOrphanedVersionedResources(ctx context.Context) (int, error) {
	d := r.store.DAOs()
	protected := r.staticResourceNames(ctx)
	enqueued := 0

	for kind, names := range map[string][]string{
		cleanup.ResourceSandboxTemplate: {protected.template, protected.pool},
		cleanup.ResourceSandboxWarmPool: {protected.pool, protected.template},
	} {
		for _, name := range names {
			if name == "" {
				continue
			}
			if n, err := d.CleanupJobs.DeleteByKindAndRef(ctx, kind, name); err != nil {
				return enqueued, fmt.Errorf("reconciler: purge stale cleanup job for protected %s %q: %w", kind, name, err)
			} else if n > 0 {
				r.log.Info("reconciler: purged stale cleanup job for protected static resource",
					"kind", kind, "ref", name, "removed", n)
			}
		}
	}

	templates, err := r.k8s.SandboxTemplates(r.cfg.K8s.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return enqueued, fmt.Errorf("reconciler: list sandbox templates: %w", err)
	}
	pools, err := r.k8s.SandboxWarmPools(r.cfg.K8s.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return enqueued, fmt.Errorf("reconciler: list warm pools: %w", err)
	}

	referenced := map[string]bool{}
	if profiles, perr := d.RuntimeProfiles.ListAll(ctx); perr == nil {
		for i := range profiles {
			if profiles[i].TemplateName != "" {
				referenced[profiles[i].TemplateName] = true
			}
			if profiles[i].WarmPoolName != "" {
				referenced[profiles[i].WarmPoolName] = true
			}
		}
	}

	for i := range templates.Items {
		name := templates.Items[i].Name
		if referenced[name] || protected.is(name) {
			continue
		}
		ok, err := r.enqueueIfAbsent(ctx, d, cleanup.ResourceSandboxTemplate, name)
		if err != nil {
			return enqueued, err
		}
		if ok {
			enqueued++
		}
	}
	for i := range pools.Items {
		name := pools.Items[i].Name
		if referenced[name] || protected.is(name) {
			continue
		}
		ok, err := r.enqueueIfAbsent(ctx, d, cleanup.ResourceSandboxWarmPool, name)
		if err != nil {
			return enqueued, err
		}
		if ok {
			enqueued++
		}
	}
	return enqueued, nil
}

type staticNames struct {
	template string
	pool     string
}

func (s staticNames) is(name string) bool {
	return name != "" && (name == s.template || name == s.pool)
}

func (r *Runner) staticResourceNames(ctx context.Context) staticNames {
	out := staticNames{pool: r.cfg.K8s.WarmPoolName}
	if out.pool != "" {
		if strings.HasSuffix(out.pool, "-warm-pool") {
			out.template = strings.TrimSuffix(out.pool, "-warm-pool") + "-runtime"
		}
		if p, err := r.k8s.SandboxWarmPools(r.cfg.K8s.Namespace).Get(ctx, out.pool, metav1.GetOptions{}); err == nil &&
			p.Spec.TemplateRef.Name != "" {
			out.template = p.Spec.TemplateRef.Name
		}
	}
	return out
}

func (r *Runner) enqueueIfAbsent(ctx context.Context, d model.DAOs, kind, ref string) (bool, error) {
	existing, err := d.CleanupJobs.ListByKindAndRef(ctx, kind, ref)
	if err != nil {
		return false, err
	}
	for i := range existing {
		if existing[i].Status != "completed" {
			return false, nil
		}
	}
	job := &model.CleanupJob{ResourceKind: kind, InternalRef: ref, DueAt: time.Now().UTC(), Status: "pending"}
	if err := d.CleanupJobs.Create(ctx, job); err != nil {
		if model.IsDuplicate(err) {
			return false, nil
		}
		return false, err
	}
	r.log.Info("reconciler: orphaned versioned resource cleanup scheduled", "kind", kind, "ref", ref)
	return true, nil
}
