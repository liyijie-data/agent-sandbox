package reconciler

import (
	"context"
	"errors"
	"fmt"

	"agent-platform/model"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (r *Runner) effectiveWarmPoolConfig(ctx context.Context) (model.WarmPoolBudget, error) {
	settings, err := r.store.DAOs().Platform.Get(ctx)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return model.WarmPoolBudget{}, err
	}
	if err != nil {
		settings = nil
	}
	return model.EffectivePlatformSettings(settings, model.WarmPoolBudget{
		Budget:      r.cfg.Platform.WarmPoolBudget,
		MaxPerImage: r.cfg.Platform.WarmPoolMaxPerImage,
		DefaultPool: r.cfg.Platform.WarmPoolDefaultPool,
	}), nil
}

func (r *Runner) desiredWarmPoolReplicas(ctx context.Context) (map[string]int32, error) {
	budget, err := r.effectiveWarmPoolConfig(ctx)
	if err != nil {
		return nil, err
	}
	images, err := r.store.DAOs().Images.ListEnabled(ctx)
	if err != nil {
		return nil, err
	}
	return model.WarmPoolAllocate(images, budget), nil
}

func (r *Runner) DriftCheckWarmPools(ctx context.Context) error {

	allImages, err := r.store.DAOs().Images.List(ctx, -1, 0)
	if err != nil {
		return err
	}
	for i := range allImages {
		if allImages[i].Status != "disabled" && allImages[i].Status != "revoked" {
			continue
		}
		profiles, perr := r.store.DAOs().RuntimeProfiles.ListByImage(ctx, allImages[i].ID)
		if perr != nil {
			return perr
		}
		for j := range profiles {
			p := &profiles[j]
			pool, gerr := r.k8s.SandboxWarmPools(r.cfg.K8s.Namespace).Get(ctx, p.WarmPoolName, metav1.GetOptions{})
			if gerr != nil {
				if isNotFound(gerr) {
					continue
				}
				return gerr
			}
			if pool.Spec.Replicas != nil && *pool.Spec.Replicas == 0 {
				continue
			}
			if e := r.ensurePool(ctx, r.renderPool(p.WarmPoolName, p.TemplateName, 0)); e != nil {
				return e
			}
		}
	}
	head, err := r.store.DAOs().PlatformNetwork.GetHead(ctx)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil
		}
		return err
	}
	if head.ActiveRevisionID == nil {
		return nil
	}
	desired, err := r.desiredWarmPoolReplicas(ctx)
	if err != nil {
		return err
	}
	images, err := r.store.DAOs().Images.ListEnabled(ctx)
	if err != nil {
		return err
	}
	revisions := make(map[string]warmPoolNetworkRevisions, len(images))
	for i := range images {
		current, rerr := r.effectiveRevisionForImage(ctx, &images[i])
		if rerr != nil {
			return rerr
		}
		desired, rerr := r.desiredRevisionForImage(ctx, &images[i])
		if rerr != nil {
			return rerr
		}
		revisions[images[i].ID] = warmPoolNetworkRevisions{current: current, desired: desired}
	}
	for i := range images {
		if images[i].Repository == nil || *images[i].Repository == "" {
			continue
		}
		if err := r.provisionCurrentImage(ctx, &images[i], *head.ActiveRevisionID, desired[images[i].ID]); err != nil {
			r.log.Error("reconciler: current image profile provisioning", "image", images[i].ID, "error", err)
		}
	}
	profiles, err := r.store.DAOs().RuntimeProfiles.ListByPlatformRevision(ctx, *head.ActiveRevisionID)
	if err != nil {
		return err
	}
	for i := range profiles {
		p := &profiles[i]

		want, ok := desired[p.ImageRegistrationID]
		if !ok {
			continue
		}
		revs, ok := revisions[p.ImageRegistrationID]
		if !ok {
			continue
		}
		current := ptrEqual(p.NetworkRevisionID, revs.current)
		pending := revs.desired != nil && ptrEqual(p.NetworkRevisionID, revs.desired)
		if p.Status == profileFailed && current {
			continue
		}
		if !current && !pending {
			want = 0
		}
		pool, gerr := r.k8s.SandboxWarmPools(r.cfg.K8s.Namespace).Get(ctx, p.WarmPoolName, metav1.GetOptions{})
		if gerr != nil {
			if isNotFound(gerr) {
				continue
			}
			return fmt.Errorf("reconciler: read warm pool %s: %w", p.WarmPoolName, gerr)
		}
		if pool.Spec.Replicas != nil && *pool.Spec.Replicas == want {
			continue
		}
		if err := r.ensurePool(ctx, r.renderPool(p.WarmPoolName, p.TemplateName, want)); err != nil {
			return fmt.Errorf("reconciler: scale warm pool %s: %w", p.WarmPoolName, err)
		}
		r.log.Info("reconciler: warm pool spec converged", "pool", p.WarmPoolName, "replicas", want)
	}
	return nil
}

type warmPoolNetworkRevisions struct {
	current *string
	desired *string
}

func (r *Runner) desiredRevisionForImage(ctx context.Context, img *model.Image) (*string, error) {
	if h, err := r.store.DAOs().NetworkHeads.GetImage(ctx, img.ID); err == nil {
		if h.RolloutStatus == "applying" && h.DesiredRevisionID != nil {
			return h.DesiredRevisionID, nil
		}
		if h.ActiveRevisionID != nil {
			return nil, nil
		}
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	if h, err := r.store.DAOs().NetworkHeads.GetClient(ctx, img.ClientID); err == nil {
		if h.RolloutStatus == "applying" {
			return h.DesiredRevisionID, nil
		}
		return nil, nil
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	return nil, nil
}

func (r *Runner) provisionCurrentImage(ctx context.Context, img *model.Image, platformRev string, replicas int32) error {
	netRev, err := r.effectiveRevisionForImage(ctx, img)
	if err != nil {
		return err
	}
	profile, err := r.ensureProfileForRevision(ctx, img, platformRev, netRev)
	if err != nil {
		return err
	}
	spec, err := r.effectiveSpec(ctx, netRev)
	if err != nil {
		return err
	}
	tpl, err := r.renderTemplate(profile.TemplateName, *img.Repository+"@"+img.Digest, spec)
	if err != nil {
		return err
	}
	if err := r.ensureTemplate(ctx, tpl); err != nil {
		_, _ = r.store.DAOs().RuntimeProfiles.UpdateStatus(ctx, profile.ID, profilePending, profileFailed)
		return err
	}
	pool := r.renderPool(profile.WarmPoolName, tpl.Name, replicas)
	if err := r.ensurePool(ctx, pool); err != nil {
		_, _ = r.store.DAOs().RuntimeProfiles.UpdateStatus(ctx, profile.ID, profilePending, profileFailed)
		return err
	}
	if err := r.waitPoolReady(ctx, pool.Name); err != nil {
		_, _ = r.store.DAOs().RuntimeProfiles.UpdateStatus(ctx, profile.ID, profilePending, profileFailed)
		return err
	}
	_, err = r.store.DAOs().RuntimeProfiles.UpdateStatus(ctx, profile.ID, profilePending, profileReady)
	return err
}

func (r *Runner) effectiveRevisionForImage(ctx context.Context, img *model.Image) (*string, error) {
	if h, err := r.store.DAOs().NetworkHeads.GetImage(ctx, img.ID); err == nil && h.ActiveRevisionID != nil {
		return h.ActiveRevisionID, nil
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	if h, err := r.store.DAOs().NetworkHeads.GetClient(ctx, img.ClientID); err == nil && h.ActiveRevisionID != nil {
		return h.ActiveRevisionID, nil
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	return nil, nil
}
