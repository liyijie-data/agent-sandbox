package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"agent-platform/internal/contracts"
	"agent-platform/model"
)

type imageState int

const (
	imageDeferred imageState = iota
	imageCompleted
	imageFailed
)

type imageOutcome struct {
	imageID string
	state   imageState
	err     string
}

func (r *Runner) processJob(ctx context.Context, job *model.RolloutJob) error {

	if job.Status != jobRunning {
		if ok, err := r.store.DAOs().RolloutJobs.MarkRunning(ctx, job.ID, job.Status); err != nil {
			return err
		} else if !ok {
			return nil
		}
		job.Status = jobRunning
	}
	images, err := func() ([]model.Image, error) {

		superseded, serr := r.jobSuperseded(ctx, job)
		if serr != nil {
			return nil, serr
		}
		if superseded {
			d := r.store.DAOs()
			if ok, cerr := d.RolloutJobs.Complete(ctx, job.ID, jobRunning); cerr != nil {
				return nil, cerr
			} else if ok {
				r.log.Info("reconciler: superseded rollout abandoned before provisioning",
					"job", job.ID, "scope", job.Scope, "revision", ptrStr(job.NetworkRevisionID))
			}
			return nil, nil
		}
		return r.resolveImages(ctx, job)
	}()
	if err != nil {
		return r.failJob(ctx, job, err.Error())
	}
	if images == nil {
		return nil
	}
	if len(images) == 0 {

		return r.promoteAndComplete(ctx, job)
	}

	if err := r.ensureRolloutRows(ctx, job, images); err != nil {
		return r.failJob(ctx, job, err.Error())
	}

	allCompleted := true
	var firstErr string
	for i := range images {
		out := r.processImage(ctx, job, &images[i])
		switch out.state {
		case imageFailed:
			allCompleted = false
			if firstErr == "" {
				firstErr = out.err
			}
		case imageDeferred:
			allCompleted = false
		}
	}
	if !allCompleted {
		if firstErr == "" {
			return nil
		}
		return r.failJob(ctx, job, firstErr)
	}
	return r.promoteAndComplete(ctx, job)
}

func (r *Runner) failJob(ctx context.Context, job *model.RolloutJob, errSummary string) error {
	d := r.store.DAOs()
	if errSummary == "" {
		errSummary = "reconciler: rollout failed"
	}
	rows, err := d.RolloutJobs.ListImages(ctx, job.ID)
	if err != nil {
		return err
	}
	for i := range rows {
		if rows[i].Status != jobCompleted {
			_, _ = d.RolloutJobs.UpdateImageStatus(ctx, rows[i].ID, rows[i].Status, jobFailed, rows[i].Attempts+1, errSummary)
			if rows[i].RuntimeProfileID != nil {
				_, _ = d.RuntimeProfiles.UpdateStatus(ctx, *rows[i].RuntimeProfileID, profilePending, profileFailed)
			}
		}
	}
	if _, err := d.RolloutJobs.Fail(ctx, job.ID, jobRunning, errSummary); err != nil {
		return err
	}
	return r.failHead(ctx, job, errSummary)
}

func (r *Runner) failHead(ctx context.Context, job *model.RolloutJob, errSummary string) error {
	switch job.Scope {
	case "client":
		return r.store.DAOs().NetworkHeads.FailClient(ctx, *job.ClientID, errSummary)
	case "image":
		return r.store.DAOs().NetworkHeads.FailImage(ctx, *job.ImageRegistrationID, errSummary)
	case "platform":
		return r.store.DAOs().PlatformNetwork.Fail(ctx, errSummary)
	}
	return fmt.Errorf("reconciler: unknown rollout scope %q", job.Scope)
}

func (r *Runner) promoteAndComplete(ctx context.Context, job *model.RolloutJob) error {
	d := r.store.DAOs()
	var expectedActive, desired *string
	var ok bool
	var err error
	switch job.Scope {
	case "client":
		head, herr := d.NetworkHeads.GetClient(ctx, *job.ClientID)
		if herr != nil {
			return herr
		}
		expectedActive, desired = head.ActiveRevisionID, job.NetworkRevisionID
		ok, err = d.NetworkHeads.PromoteClient(ctx, *job.ClientID, expectedActive, desired)
	case "image":
		head, herr := d.NetworkHeads.GetImage(ctx, *job.ImageRegistrationID)
		if herr != nil {
			return herr
		}
		expectedActive, desired = head.ActiveRevisionID, job.NetworkRevisionID
		ok, err = d.NetworkHeads.PromoteImage(ctx, *job.ImageRegistrationID, expectedActive, desired)
	case "platform":
		head, herr := d.PlatformNetwork.GetHead(ctx)
		if herr != nil {
			return herr
		}
		expectedActive, desired = head.ActiveRevisionID, job.PlatformRevisionID
		ok, err = d.PlatformNetwork.Promote(ctx, expectedActive, desired)
	default:
		return fmt.Errorf("reconciler: unknown rollout scope %q", job.Scope)
	}
	if err != nil {
		return err
	}
	if !ok {
		return r.handlePromotionLoss(ctx, job, expectedActive, desired)
	}
	if ok, cerr := d.RolloutJobs.Complete(ctx, job.ID, jobRunning); cerr != nil {
		return cerr
	} else if !ok {
		return nil
	}
	r.log.Info("reconciler: rollout completed and head advanced",
		"job", job.ID, "scope", job.Scope, "desired", ptrStr(desired))
	return nil
}

func (r *Runner) handlePromotionLoss(ctx context.Context, job *model.RolloutJob, expectedActive, desired *string) error {
	d := r.store.DAOs()
	var headActive *string
	var headDesired *string
	switch job.Scope {
	case "client":
		head, err := d.NetworkHeads.GetClient(ctx, *job.ClientID)
		if err != nil {
			return err
		}
		headActive, headDesired = head.ActiveRevisionID, head.DesiredRevisionID
	case "image":
		head, err := d.NetworkHeads.GetImage(ctx, *job.ImageRegistrationID)
		if err != nil {
			return err
		}
		headActive, headDesired = head.ActiveRevisionID, head.DesiredRevisionID
	case "platform":
		head, err := d.PlatformNetwork.GetHead(ctx)
		if err != nil {
			return err
		}
		headActive, headDesired = head.ActiveRevisionID, head.DesiredRevisionID
	}
	if !ptrEqual(headActive, expectedActive) {

		if ok, err := d.RolloutJobs.Complete(ctx, job.ID, jobRunning); err != nil {
			return err
		} else if ok {
			r.log.Info("reconciler: superseded rollout completed (head already advanced)",
				"job", job.ID, "scope", job.Scope, "desired", ptrStr(desired))
		}
		return nil
	}
	if !ptrEqual(headDesired, desired) {

		if ok, err := d.RolloutJobs.Complete(ctx, job.ID, jobRunning); err != nil {
			return err
		} else if ok {
			r.log.Info("reconciler: superseded rollout completed (desired advanced)",
				"job", job.ID, "scope", job.Scope, "desired", ptrStr(desired), "active", ptrStr(headActive))
		}
		return nil
	}
	if job.Scope == "platform" && !ptrEqual(headActive, job.PlatformRevisionID) {

		if ok, err := d.RolloutJobs.Complete(ctx, job.ID, jobRunning); err != nil {
			return err
		} else if ok {
			r.log.Info("reconciler: platform job superseded by newer baseline (completed)",
				"job", job.ID, "target", ptrStr(job.PlatformRevisionID), "active", ptrStr(headActive))
		}
		return nil
	}

	r.log.Info("reconciler: rollout promotion lost; waiting", "job", job.ID, "scope", job.Scope)
	return nil
}

func (r *Runner) jobSuperseded(ctx context.Context, job *model.RolloutJob) (bool, error) {
	if job.NetworkRevisionID == nil && job.Scope != "platform" {
		return false, nil
	}
	d := r.store.DAOs()
	switch job.Scope {
	case "client":
		head, err := d.NetworkHeads.GetClient(ctx, *job.ClientID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return false, nil
			}
			return false, err
		}
		return head.DesiredRevisionID != nil && !ptrEqual(head.DesiredRevisionID, job.NetworkRevisionID), nil
	case "image":
		head, err := d.NetworkHeads.GetImage(ctx, *job.ImageRegistrationID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return false, nil
			}
			return false, err
		}
		return head.DesiredRevisionID != nil && !ptrEqual(head.DesiredRevisionID, job.NetworkRevisionID), nil
	case "platform":
		head, err := d.PlatformNetwork.GetHead(ctx)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				return false, nil
			}
			return false, err
		}
		return head.DesiredRevisionID != nil && !ptrEqual(head.DesiredRevisionID, job.PlatformRevisionID), nil
	}
	return false, nil
}

func (r *Runner) resolveImages(ctx context.Context, job *model.RolloutJob) ([]model.Image, error) {
	switch job.Scope {
	case "client":
		return r.store.DAOs().Images.ListEnabledByClient(ctx, *job.ClientID)
	case "image":
		img, err := r.store.DAOs().Images.Get(ctx, *job.ImageRegistrationID)
		if err != nil {
			return nil, err
		}
		return []model.Image{*img}, nil
	case "platform":
		return r.store.DAOs().Images.ListEnabled(ctx)
	}
	return nil, fmt.Errorf("reconciler: unknown rollout scope %q", job.Scope)
}

func (r *Runner) ensureRolloutRows(ctx context.Context, job *model.RolloutJob, images []model.Image) error {
	existing, err := r.store.DAOs().RolloutJobs.ListImages(ctx, job.ID)
	if err != nil {
		return err
	}
	byImage := map[string]bool{}
	for i := range existing {
		byImage[existing[i].ImageRegistrationID] = true
	}
	for i := range images {
		img := &images[i]
		if byImage[img.ID] {
			continue
		}
		row := &model.RolloutJobImage{RolloutJobID: job.ID, ImageRegistrationID: img.ID, Status: jobPending}
		if img.Repository != nil && *img.Repository != "" {
			netRevID, rerr := r.effectiveRevision(ctx, job, img)
			if rerr != nil {
				return rerr
			}

			profile, perr := r.ensureProfile(ctx, job, img, netRevID)
			if perr != nil {
				return perr
			}
			row.RuntimeProfileID = &profile.ID
		}
		if err := r.store.DAOs().RolloutJobs.CreateImage(ctx, row); err != nil {
			if !model.IsDuplicate(err) {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) effectiveRevision(ctx context.Context, job *model.RolloutJob, img *model.Image) (*string, error) {
	if job.NetworkRevisionID != nil {
		return job.NetworkRevisionID, nil
	}
	if job.Scope != "platform" {
		return nil, nil
	}
	if ih, err := r.store.DAOs().NetworkHeads.GetImage(ctx, img.ID); err == nil && ih.ActiveRevisionID != nil {
		return ih.ActiveRevisionID, nil
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	if ch, err := r.store.DAOs().NetworkHeads.GetClient(ctx, img.ClientID); err == nil && ch.ActiveRevisionID != nil {
		return ch.ActiveRevisionID, nil
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	return nil, nil
}

func (r *Runner) effectiveSpec(ctx context.Context, netRevID *string) (contracts.NetworkConfigSpec, error) {
	if netRevID == nil {
		return contracts.NetworkConfigSpec{}, nil
	}
	rev, err := r.store.DAOs().NetworkConfigs.Get(ctx, *netRevID)
	if err != nil {
		return contracts.NetworkConfigSpec{}, err
	}
	var spec contracts.NetworkConfigSpec
	if err := json.Unmarshal([]byte(rev.Config), &spec); err != nil {
		return contracts.NetworkConfigSpec{}, fmt.Errorf("reconciler: decode network config %s: %w", *netRevID, err)
	}
	return spec, nil
}

func (r *Runner) ensureProfile(ctx context.Context, job *model.RolloutJob, img *model.Image, netRevID *string) (*model.RuntimeProfile, error) {
	platformRevID := *job.PlatformRevisionID
	return r.ensureProfileForRevision(ctx, img, platformRevID, netRevID)
}

func (r *Runner) ensureProfileForRevision(ctx context.Context, img *model.Image, platformRevID string, netRevID *string) (*model.RuntimeProfile, error) {
	d := r.store.DAOs()
	imageRef := *img.Repository + "@" + img.Digest
	tplName := templateName(r.cfg.K8s.TemplateNamePrefix, img.ID, platformRevID, netRevID)
	pool := poolName(r.cfg.K8s.TemplateNamePrefix, img.ID, platformRevID, netRevID)
	p, err := d.RuntimeProfiles.GetByTriple(ctx, img.ID, platformRevID, netRevID)
	if err == nil {
		if p.Status == profileFailed {
			if ok, uerr := d.RuntimeProfiles.UpdateStatus(ctx, p.ID, profileFailed, profilePending); ok && uerr == nil {
				p.Status = profilePending
			}
		}
		if p.TemplateName != tplName || p.WarmPoolName != pool {
			_, _ = d.RuntimeProfiles.UpdateNames(ctx, p.ID, tplName, pool)
		}
		return p, nil
	}
	if !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	p = &model.RuntimeProfile{ImageRegistrationID: img.ID, PlatformRevisionID: platformRevID,
		NetworkRevisionID: netRevID, ImageRef: imageRef, TemplateName: tplName, WarmPoolName: pool,
		Status: profilePending}
	if err := d.RuntimeProfiles.Create(ctx, p); err != nil {
		if model.IsDuplicate(err) {
			return d.RuntimeProfiles.GetByTriple(ctx, img.ID, platformRevID, netRevID)
		}
		return nil, err
	}
	return p, nil
}

func (r *Runner) processImage(ctx context.Context, job *model.RolloutJob, img *model.Image) (out imageOutcome) {
	out = imageOutcome{imageID: img.ID, state: imageFailed}
	fail := func(summary string) imageOutcome {
		out.err = summary
		r.failImage(ctx, job, img, summary)
		return out
	}
	d := r.store.DAOs()

	row, err := r.rolloutRow(ctx, job, img)
	if err != nil {
		return fail("resolve rollout ledger row: " + err.Error())
	}
	if row.Status == jobCompleted {
		out.state = imageCompleted
		return out
	}
	if img.Repository == nil || *img.Repository == "" {
		return fail("image has no registered repository; cannot build repository@digest ref")
	}
	imageRef := *img.Repository + "@" + img.Digest
	netRevID, err := r.effectiveRevision(ctx, job, img)
	if err != nil {
		return fail("resolve effective network revision: " + err.Error())
	}
	spec, err := r.effectiveSpec(ctx, netRevID)
	if err != nil {
		return fail("resolve effective network config: " + err.Error())
	}
	profile, err := r.ensureProfile(ctx, job, img, netRevID)
	if err != nil {
		return fail("ensure runtime profile: " + err.Error())
	}
	if row.Status != jobRunning {
		if ok, uerr := d.RolloutJobs.UpdateImageStatus(ctx, row.ID, row.Status, jobRunning, row.Attempts, ""); !ok || uerr != nil {

			out.state = imageDeferred
			return out
		}
	}
	platformRevID := *job.PlatformRevisionID
	tpl, err := r.renderTemplate(templateName(r.cfg.K8s.TemplateNamePrefix, img.ID, platformRevID, netRevID), imageRef, spec)
	if err != nil {
		return fail("render template: " + err.Error())
	}
	if err := r.ensureTemplate(ctx, tpl); err != nil {
		return fail("create sandbox template " + tpl.Name + ": " + err.Error())
	}

	desired, err := r.desiredWarmPoolReplicas(ctx)
	if err != nil {
		return fail("resolve warm pool replicas: " + err.Error())
	}
	replicas := desired[img.ID]
	pool := r.renderPool(poolName(r.cfg.K8s.TemplateNamePrefix, img.ID, platformRevID, netRevID), tpl.Name, replicas)
	if err := r.ensurePool(ctx, pool); err != nil {
		return fail("create warm pool " + pool.Name + ": " + err.Error())
	}
	if err := r.waitPoolReady(ctx, pool.Name); err != nil {
		return fail("warm pool " + pool.Name + ": " + err.Error())
	}
	if ok, uerr := d.RolloutJobs.UpdateImageStatus(ctx, row.ID, jobRunning, jobCompleted, row.Attempts, ""); !ok || uerr != nil {
		return out
	}
	if ok, uerr := d.RuntimeProfiles.UpdateStatus(ctx, profile.ID, profilePending, profileReady); uerr != nil {
		return fail("mark profile ready: " + uerr.Error())
	} else if !ok {

		cur, gerr := d.RuntimeProfiles.Get(ctx, profile.ID)
		if gerr == nil && cur.Status == profileFailed {
			return fail("runtime profile marked failed concurrently")
		}
	}
	out.state = imageCompleted
	out.err = ""
	r.log.Info("reconciler: image pool ready", "job", job.ID, "image", img.ID, "template", tpl.Name, "pool", pool.Name)
	return out
}

func (r *Runner) rolloutRow(ctx context.Context, job *model.RolloutJob, img *model.Image) (*model.RolloutJobImage, error) {
	rows, err := r.store.DAOs().RolloutJobs.ListImages(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].ImageRegistrationID == img.ID {
			return &rows[i], nil
		}
	}
	return nil, fmt.Errorf("reconciler: rollout job %s has no ledger row for image %s", job.ID, img.ID)
}

func (r *Runner) failImage(ctx context.Context, job *model.RolloutJob, img *model.Image, summary string) {
	d := r.store.DAOs()
	rows, err := d.RolloutJobs.ListImages(ctx, job.ID)
	if err != nil {
		return
	}
	for i := range rows {
		if rows[i].ImageRegistrationID != img.ID {
			continue
		}
		if rows[i].Status != jobCompleted {
			_, _ = d.RolloutJobs.UpdateImageStatus(ctx, rows[i].ID, rows[i].Status, jobFailed, rows[i].Attempts+1, summary)
		}
		if rows[i].RuntimeProfileID != nil {
			_, _ = d.RuntimeProfiles.UpdateStatus(ctx, *rows[i].RuntimeProfileID, profilePending, profileFailed)
		}
		return
	}
}

func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func ptrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
