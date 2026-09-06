package cleanup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"agent-platform/internal/executor"
	"agent-platform/model"
)

type DBContentDeleter struct {
	Contents model.RunContentDAO
}

func (d DBContentDeleter) DeleteContent(ctx context.Context, runID string) (DeletionOutcome, error) {
	now := time.Now().UTC()
	remaining, err := d.Contents.CountExpiredByRun(ctx, runID, now)
	if err != nil {
		return 0, err
	}
	if remaining == 0 {

		return OutcomeAlreadyGone, nil
	}
	if _, err := d.Contents.DeleteExpiredByRun(ctx, runID, now); err != nil {
		return 0, err
	}

	left, err := d.Contents.CountExpiredByRun(ctx, runID, now)
	if err != nil {
		return 0, err
	}
	if left > 0 {
		return 0, fmt.Errorf("cleanup: %d expired rows remain for run %s (deletion not verified)", left, runID)
	}
	return OutcomeDeleted, nil
}

type RecoveryObjectDeleter struct {
	Store executor.RecoveryStore
}

func (d RecoveryObjectDeleter) DeleteObject(ctx context.Context, objectRef string) (DeletionOutcome, error) {
	ok, err := d.Store.Exists(ctx, objectRef)
	if err != nil {
		return 0, err
	}
	if !ok {
		return OutcomeAlreadyGone, nil
	}
	if err := d.Store.Delete(ctx, objectRef); err != nil {
		return 0, err
	}
	ok, err = d.Store.Exists(ctx, objectRef)
	if err != nil {
		return 0, err
	}
	if ok {
		return 0, fmt.Errorf("cleanup: object %q still exists after deletion", objectRef)
	}
	return OutcomeDeleted, nil
}

type DBProfileDeleter struct {
	Profiles model.RuntimeProfileDAO
	Stages   model.RunStageDAO
	Jobs     model.RolloutJobDAO
	Images   *model.ImageDAO

	Heads *model.NetworkConfigHeadDAO
	K8s   K8sResourceDeleter
}

func (d DBProfileDeleter) DeleteProfile(ctx context.Context, profileID string) (DeletionOutcome, error) {
	p, err := d.Profiles.Get(ctx, profileID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return OutcomeAlreadyGone, nil
		}
		return 0, err
	}

	refs, err := d.Stages.CountRuntimeProfileRefs(ctx, profileID)
	if err != nil {
		return 0, err
	}
	if refs > 0 {
		return 0, ErrReferenced
	}
	refs, err = d.Jobs.CountProfileRefs(ctx, profileID)
	if err != nil {
		return 0, err
	}
	if refs > 0 {
		return 0, ErrReferenced
	}

	if d.Heads != nil && p.NetworkRevisionID != nil && d.Images != nil {
		imgRow, ierr := d.Images.Get(ctx, p.ImageRegistrationID)
		if ierr == nil {
			desired, derr := desiredNetworkRevisionForClient(ctx, *d.Heads, imgRow.ClientID)
			if derr == nil && desired != nil && *desired == *p.NetworkRevisionID {
				return 0, ErrReferenced
			}
		}
	}

	if d.Images != nil {
		img, ierr := d.Images.Get(ctx, p.ImageRegistrationID)
		if ierr == nil {
			if img.Status == "revoked" || img.Status == "disabled" {
				_, _ = d.Profiles.MarkConvergedStatus(ctx, profileID, img.Status)
			}
		}
	}

	if d.K8s != nil {
		if p.TemplateName != "" {
			if _, err := d.K8s.DeleteK8sObject(ctx, ResourceSandboxTemplate, p.TemplateName); err != nil {
				return 0, err
			}
		}
		if p.WarmPoolName != "" {
			if _, err := d.K8s.DeleteK8sObject(ctx, ResourceSandboxWarmPool, p.WarmPoolName); err != nil {
				return 0, err
			}
		}
	}
	return OutcomeDeleted, nil
}

type DBOrphanObjectDeleter struct {
	Store       ObjectStoreAccess
	Checkpoints model.CheckpointDAO
}

type ObjectStoreAccess interface {
	Exists(ctx context.Context, objectKey string) (bool, error)
	Delete(ctx context.Context, objectKey string) error
}

func (d DBOrphanObjectDeleter) DeleteOrphanObject(ctx context.Context, objectKey string) (DeletionOutcome, error) {
	refs, err := d.Checkpoints.CountRefsByObject(ctx, objectKey)
	if err != nil {
		return 0, err
	}
	if refs > 0 {
		return 0, ErrReferenced
	}
	ok, err := d.Store.Exists(ctx, objectKey)
	if err != nil {
		return 0, err
	}
	if !ok {
		return OutcomeAlreadyGone, nil
	}
	if err := d.Store.Delete(ctx, objectKey); err != nil {
		return 0, err
	}
	ok, err = d.Store.Exists(ctx, objectKey)
	if err != nil {
		return 0, err
	}
	if ok {
		return 0, fmt.Errorf("cleanup: orphan object %q still exists after deletion", objectKey)
	}
	return OutcomeDeleted, nil
}

func desiredNetworkRevisionForClient(ctx context.Context, heads model.NetworkConfigHeadDAO, clientID string) (*string, error) {
	ch, err := heads.GetClient(ctx, clientID)
	if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	if err == nil && ch.DesiredRevisionID != nil {
		return ch.DesiredRevisionID, nil
	}
	return nil, nil
}
