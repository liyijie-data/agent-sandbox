package reconciler

import (
	"context"
	"errors"
	"fmt"

	"agent-platform/model"
)

var ErrProfileReferenced = errors.New("reconciler: runtime profile is referenced by run stages")

func References(ctx context.Context, store *model.Store, profileID string) (int, error) {
	return store.DAOs().Stages.CountRuntimeProfileRefs(ctx, profileID)
}

func ProfileReferenced(ctx context.Context, store *model.Store, profileID string) (bool, error) {
	n, err := References(ctx, store, profileID)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func EnsureUnreferenced(ctx context.Context, store *model.Store, profileID string) error {
	refd, err := ProfileReferenced(ctx, store, profileID)
	if err != nil {
		return err
	}
	if refd {
		return fmt.Errorf("%w: %s", ErrProfileReferenced, profileID)
	}
	return nil
}
