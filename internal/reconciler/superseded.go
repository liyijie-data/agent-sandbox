package reconciler

import (
	"context"
	"errors"
	"fmt"

	"agent-platform/internal/cleanup"
	"agent-platform/model"
)

func (r *Runner) EnqueueSupersededProfileCleanup(ctx context.Context) (int, error) {
	d := r.store.DAOs()
	head, err := d.PlatformNetwork.GetHead(ctx)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if head.ActiveRevisionID == nil {
		return 0, nil
	}
	profiles, err := d.RuntimeProfiles.ListAll(ctx)
	if err != nil {
		return 0, err
	}
	enqueued := 0
	for i := range profiles {
		p := &profiles[i]
		if p.PlatformRevisionID == *head.ActiveRevisionID &&
			(r.isCurrentNetworkRevision(ctx, p) || r.isDesiredNetworkRevision(ctx, p)) &&
			r.imageEnabled(ctx, p.ImageRegistrationID) {
			continue
		}
		ok, err := r.enqueueIfAbsent(ctx, d, cleanup.ResourceRuntimeProfile, p.ID)
		if err != nil {
			return enqueued, fmt.Errorf("reconciler: enqueue superseded profile %s: %w", p.ID, err)
		}
		if ok {
			enqueued++
		}
	}
	return enqueued, nil
}

func (r *Runner) isCurrentNetworkRevision(ctx context.Context, p *model.RuntimeProfile) bool {
	img, err := r.store.DAOs().Images.Get(ctx, p.ImageRegistrationID)
	if err != nil {
		return false
	}
	net, err := r.effectiveRevision(ctx, &model.RolloutJob{Scope: "platform"}, img)
	if err != nil {
		return false
	}
	if p.NetworkRevisionID == nil || net == nil {
		return p.NetworkRevisionID == nil && net == nil
	}
	return *p.NetworkRevisionID == *net
}

func (r *Runner) imageEnabled(ctx context.Context, imageID string) bool {
	img, err := r.store.DAOs().Images.Get(ctx, imageID)
	return err == nil && img.Status == "enabled"
}

func (r *Runner) isDesiredNetworkRevision(ctx context.Context, p *model.RuntimeProfile) bool {
	img, err := r.store.DAOs().Images.Get(ctx, p.ImageRegistrationID)
	if err != nil {
		return false
	}
	if ih, err := r.store.DAOs().NetworkHeads.GetImage(ctx, img.ID); err == nil && ih.DesiredRevisionID != nil {
		if p.NetworkRevisionID == nil {
			return false
		}
		return *p.NetworkRevisionID == *ih.DesiredRevisionID
	}
	if ch, err := r.store.DAOs().NetworkHeads.GetClient(ctx, img.ClientID); err == nil && ch.DesiredRevisionID != nil {
		if p.NetworkRevisionID == nil {
			return false
		}
		return *p.NetworkRevisionID == *ch.DesiredRevisionID
	}
	return false
}
