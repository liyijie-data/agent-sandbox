package networks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"agent-platform/internal/contracts"
	"agent-platform/internal/security"
	"agent-platform/model"
)

const (
	statusApplying = "applying"
	statusReady    = "ready"
	statusFailed   = "failed"
)

var (
	ErrNotFound = errors.New("networks: not found")

	ErrReqIDConflict = errors.New("networks: req id conflict")

	ErrCASConflict = errors.New("networks: expected active revision mismatch")

	ErrNetworkRejected = errors.New("networks: network configuration rejected")
)

type Service struct {
	store    *model.Store
	baseline *security.PlatformNetworkBaseline
	log      *slog.Logger
}

func New(store *model.Store, baseline *security.PlatformNetworkBaseline, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, baseline: baseline, log: log}
}

type ConfigView struct {
	Scope         string
	Source        string
	RevisionID    string
	Revision      int64
	RolloutStatus string
	Spec          contracts.NetworkConfigSpec
}

type PutResult struct {
	Idempotent    bool
	RevisionID    string
	Revision      int64
	RolloutStatus string
}

func (s *Service) GetClientConfig(ctx context.Context, clientID string) (*ConfigView, error) {
	head, err := s.store.DAOs().NetworkHeads.GetClient(ctx, clientID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return s.defaultView("client"), nil
		}
		return nil, err
	}
	if head.ActiveRevisionID == nil {
		return &ConfigView{Scope: "client", Source: "client", RolloutStatus: head.RolloutStatus, Spec: contracts.NetworkConfigSpec{}}, nil
	}
	rev, err := s.store.DAOs().NetworkConfigs.Get(ctx, *head.ActiveRevisionID)
	if err != nil {
		return nil, err
	}
	spec, err := decodeSpec(rev.Config)
	if err != nil {
		return nil, err
	}
	return &ConfigView{Scope: "client", Source: "client", RevisionID: rev.ID, Revision: rev.Revision,
		RolloutStatus: head.RolloutStatus, Spec: *spec}, nil
}

func (s *Service) GetImageConfig(ctx context.Context, clientID, imageID string) (*ConfigView, error) {
	img, err := s.store.DAOs().Images.GetByClientAndImageID(ctx, clientID, imageID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	ih, err := s.store.DAOs().NetworkHeads.GetImage(ctx, img.ID)
	if err == nil && ih.ActiveRevisionID != nil {
		rev, err := s.store.DAOs().NetworkConfigs.Get(ctx, *ih.ActiveRevisionID)
		if err != nil {
			return nil, err
		}
		spec, err := decodeSpec(rev.Config)
		if err != nil {
			return nil, err
		}
		return &ConfigView{Scope: "image", Source: "image", RevisionID: rev.ID, Revision: rev.Revision,
			RolloutStatus: ih.RolloutStatus, Spec: *spec}, nil
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}

	ch, err := s.store.DAOs().NetworkHeads.GetClient(ctx, clientID)
	if err == nil && ch.ActiveRevisionID != nil {
		rev, err := s.store.DAOs().NetworkConfigs.Get(ctx, *ch.ActiveRevisionID)
		if err != nil {
			return nil, err
		}
		spec, err := decodeSpec(rev.Config)
		if err != nil {
			return nil, err
		}
		return &ConfigView{Scope: "image", Source: "client", RevisionID: rev.ID, Revision: rev.Revision,
			RolloutStatus: ch.RolloutStatus, Spec: *spec}, nil
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	return s.defaultView("image"), nil
}

func (s *Service) PutClientConfig(ctx context.Context, clientID string, in contracts.NetworkConfigRequest) (*PutResult, error) {
	cfgJSON, hash, err := s.canonicalConfig(in)
	if err != nil {
		return nil, err
	}
	var res *PutResult
	err = s.store.Transaction(ctx, func(d model.DAOs) error {
		if job, jerr := d.RolloutJobs.GetByRequest(ctx, "client", in.ReqID, clientID, ""); jerr == nil {
			return s.reconcile(ctx, d, "client", clientID, "", job, hash, &res)
		} else if !errors.Is(jerr, model.ErrNotFound) {
			return jerr
		}

		if cerr := s.checkClientCAS(ctx, d, clientID, in.ExpectedActiveRevision); cerr != nil {
			return cerr
		}

		if err := d.NetworkHeads.ClearImageAll(ctx, clientID); err != nil {
			return err
		}

		ch, herr := d.NetworkHeads.GetClient(ctx, clientID)
		if herr == nil && ch.DesiredRevisionID != nil {
			rev, rerr := d.NetworkConfigs.Get(ctx, *ch.DesiredRevisionID)
			if rerr != nil && !errors.Is(rerr, model.ErrNotFound) {
				return rerr
			}
			if rerr == nil && bytes.Equal(rev.ConfigHash, hash) {
				res = &PutResult{Idempotent: true, RevisionID: rev.ID, Revision: rev.Revision,
					RolloutStatus: ch.RolloutStatus}
				return nil
			}
		} else if herr != nil && !errors.Is(herr, model.ErrNotFound) {
			return herr
		}
		platformRev, err := s.platformRevision(ctx, d)
		if err != nil {
			return err
		}
		next := nextRevision(revisionsOf(d.NetworkConfigs.List(ctx, "client", clientID)))
		rev := &model.NetworkConfigRevision{Scope: "client", ClientID: clientID, Revision: next,
			Config: string(cfgJSON), ConfigHash: hash}
		if err := d.NetworkConfigs.Create(ctx, rev); err != nil {
			return err
		}
		if err := s.setClientHead(ctx, d, clientID, rev.ID, in.ExpectedActiveRevision); err != nil {
			return err
		}

		if err := d.NetworkHeads.ClearImageAll(ctx, clientID); err != nil {
			return err
		}
		job := &model.RolloutJob{Scope: "client", ClientID: &clientID, RequestID: in.ReqID,
			PlatformRevisionID: platformRev, NetworkRevisionID: &rev.ID, Status: "pending"}
		if err := d.RolloutJobs.Create(ctx, job); err != nil {
			if model.IsDuplicate(err) {
				return ErrReqIDConflict
			}
			return err
		}
		res = &PutResult{RevisionID: rev.ID, Revision: rev.Revision, RolloutStatus: statusApplying}
		return nil
	})
	return res, err
}

func (s *Service) PutImageConfig(ctx context.Context, clientID, imageID string, in contracts.NetworkConfigRequest) (*PutResult, error) {
	img, err := s.store.DAOs().Images.GetByClientAndImageID(ctx, clientID, imageID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	cfgJSON, hash, err := s.canonicalConfig(in)
	if err != nil {
		return nil, err
	}
	var res *PutResult
	err = s.store.Transaction(ctx, func(d model.DAOs) error {
		if job, jerr := d.RolloutJobs.GetByRequest(ctx, "image", in.ReqID, clientID, img.ID); jerr == nil {
			return s.reconcile(ctx, d, "image", clientID, img.ID, job, hash, &res)
		} else if !errors.Is(jerr, model.ErrNotFound) {
			return jerr
		}

		if cerr := s.checkImageCAS(ctx, d, img.ID, in.ExpectedActiveRevision); cerr != nil {
			return cerr
		}

		ih, herr := d.NetworkHeads.GetImage(ctx, img.ID)
		if herr == nil && ih.DesiredRevisionID != nil {
			rev, rerr := d.NetworkConfigs.Get(ctx, *ih.DesiredRevisionID)
			if rerr != nil && !errors.Is(rerr, model.ErrNotFound) {
				return rerr
			}
			if rerr == nil && bytes.Equal(rev.ConfigHash, hash) {
				res = &PutResult{Idempotent: true, RevisionID: rev.ID, Revision: rev.Revision,
					RolloutStatus: ih.RolloutStatus}
				return nil
			}
		} else if herr != nil && !errors.Is(herr, model.ErrNotFound) {
			return herr
		}
		platformRev, err := s.platformRevision(ctx, d)
		if err != nil {
			return err
		}
		next := nextRevision(revisionsOf(d.NetworkConfigs.ListByImage(ctx, img.ID)))
		rev := &model.NetworkConfigRevision{Scope: "image", ClientID: clientID, ImageRegistrationID: &img.ID,
			Revision: next, Config: string(cfgJSON), ConfigHash: hash}
		if err := d.NetworkConfigs.Create(ctx, rev); err != nil {
			return err
		}
		if err := s.setImageHead(ctx, d, img.ID, rev.ID, in.ExpectedActiveRevision); err != nil {
			return err
		}
		job := &model.RolloutJob{Scope: "image", ClientID: &clientID, ImageRegistrationID: &img.ID, RequestID: in.ReqID,
			PlatformRevisionID: platformRev, NetworkRevisionID: &rev.ID, Status: "pending"}
		if err := d.RolloutJobs.Create(ctx, job); err != nil {
			if model.IsDuplicate(err) {
				return ErrReqIDConflict
			}
			return err
		}
		res = &PutResult{RevisionID: rev.ID, Revision: rev.Revision, RolloutStatus: statusApplying}
		return nil
	})
	return res, err
}

func (s *Service) EffectiveRevisionFor(ctx context.Context, clientID, imageID string) (*string, error) {
	img, err := s.store.DAOs().Images.GetByClientAndImageID(ctx, clientID, imageID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if ih, err := s.store.DAOs().NetworkHeads.GetImage(ctx, img.ID); err == nil && ih.ActiveRevisionID != nil {
		return ih.ActiveRevisionID, nil
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	if ch, err := s.store.DAOs().NetworkHeads.GetClient(ctx, clientID); err == nil && ch.ActiveRevisionID != nil {
		return ch.ActiveRevisionID, nil
	} else if err != nil && !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	return nil, nil
}

func (s *Service) BlockedForRun(ctx context.Context, clientID, imageID string) (bool, error) {
	img, err := s.store.DAOs().Images.GetByClientAndImageID(ctx, clientID, imageID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if ih, err := s.store.DAOs().NetworkHeads.GetImage(ctx, img.ID); err == nil {
		if ih.RolloutStatus == statusApplying || ih.RolloutStatus == statusFailed {
			return true, nil
		}
	} else if !errors.Is(err, model.ErrNotFound) {
		return false, err
	}
	if ch, err := s.store.DAOs().NetworkHeads.GetClient(ctx, clientID); err == nil {
		if ch.RolloutStatus == statusApplying || ch.RolloutStatus == statusFailed {
			return true, nil
		}
	} else if !errors.Is(err, model.ErrNotFound) {
		return false, err
	}
	return false, nil
}

func (s *Service) reconcile(ctx context.Context, d model.DAOs, scope, clientID, imageID string, job *model.RolloutJob, hash []byte, res **PutResult) error {
	if job.NetworkRevisionID == nil {
		return ErrReqIDConflict
	}
	rev, err := d.NetworkConfigs.Get(ctx, *job.NetworkRevisionID)
	if err != nil {
		return err
	}
	if !bytes.Equal(rev.ConfigHash, hash) {
		return ErrReqIDConflict
	}
	status := statusApplying
	if scope == "image" {
		if ih, herr := d.NetworkHeads.GetImage(ctx, imageID); herr == nil && ih.RolloutStatus != "" {
			status = ih.RolloutStatus
		}
	} else if ch, herr := d.NetworkHeads.GetClient(ctx, clientID); herr == nil && ch.RolloutStatus != "" {
		status = ch.RolloutStatus
	}
	*res = &PutResult{Idempotent: true, RevisionID: rev.ID, Revision: rev.Revision, RolloutStatus: status}
	return nil
}

func (s *Service) checkClientCAS(ctx context.Context, d model.DAOs, clientID string, expected *string) error {
	ch, err := d.NetworkHeads.GetClient(ctx, clientID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			if expected != nil {
				return ErrCASConflict
			}
			return nil
		}
		return err
	}
	if expected == nil {
		if ch.ActiveRevisionID != nil {
			return ErrCASConflict
		}
		return nil
	}
	if ch.ActiveRevisionID == nil || *ch.ActiveRevisionID != *expected {
		return ErrCASConflict
	}
	return nil
}

func (s *Service) checkImageCAS(ctx context.Context, d model.DAOs, imageRegID string, expected *string) error {
	ih, err := d.NetworkHeads.GetImage(ctx, imageRegID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			if expected != nil {
				return ErrCASConflict
			}
			return nil
		}
		return err
	}
	if expected == nil {
		if ih.ActiveRevisionID != nil {
			return ErrCASConflict
		}
		return nil
	}
	if ih.ActiveRevisionID == nil || *ih.ActiveRevisionID != *expected {
		return ErrCASConflict
	}
	return nil
}

func (s *Service) setClientHead(ctx context.Context, d model.DAOs, clientID, desiredID string, expected *string) error {
	if _, err := d.NetworkHeads.GetClient(ctx, clientID); err != nil {
		if !errors.Is(err, model.ErrNotFound) {
			return err
		}

		if expected != nil {
			return ErrCASConflict
		}
		return d.NetworkHeads.CreateClient(ctx, &model.NetworkConfigHead{ClientID: clientID,
			DesiredRevisionID: &desiredID, UpdatedAt: time.Now(), RolloutStatus: statusApplying})
	}
	ok, err := d.NetworkHeads.SetDesiredClient(ctx, &model.NetworkConfigHead{ClientID: clientID,
		DesiredRevisionID: &desiredID, UpdatedAt: time.Now()}, expected)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCASConflict
	}
	return nil
}

func (s *Service) setImageHead(ctx context.Context, d model.DAOs, imageRegID, desiredID string, expected *string) error {
	if _, err := d.NetworkHeads.GetImage(ctx, imageRegID); err != nil {
		if !errors.Is(err, model.ErrNotFound) {
			return err
		}
		if expected != nil {
			return ErrCASConflict
		}
		return d.NetworkHeads.CreateImage(ctx, &model.ImageNetworkHead{ImageRegistrationID: imageRegID,
			DesiredRevisionID: &desiredID, UpdatedAt: time.Now(), RolloutStatus: statusApplying})
	}
	ok, err := d.NetworkHeads.UpdateImage(ctx, &model.ImageNetworkHead{ImageRegistrationID: imageRegID,
		DesiredRevisionID: &desiredID, UpdatedAt: time.Now()}, expected)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCASConflict
	}
	return nil
}

func (s *Service) platformRevision(ctx context.Context, d model.DAOs) (*string, error) {
	head, err := d.PlatformNetwork.GetHead(ctx)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, fmt.Errorf("networks: platform network head missing")
		}
		return nil, err
	}
	if head.ActiveRevisionID == nil {
		return nil, fmt.Errorf("networks: no active platform baseline revision")
	}
	return head.ActiveRevisionID, nil
}

func (s *Service) defaultView(scope string) *ConfigView {
	return &ConfigView{Scope: scope, Source: "default", RolloutStatus: statusReady, Spec: contracts.NetworkConfigSpec{}}
}

func revisionsOf(rows []model.NetworkConfigRevision, err error) []model.NetworkConfigRevision {
	if err != nil {
		return nil
	}
	return rows
}

func nextRevision(rows []model.NetworkConfigRevision) int64 {
	next := int64(1)
	for _, r := range rows {
		if r.Revision >= next {
			next = r.Revision + 1
		}
	}
	return next
}

func decodeSpec(raw string) (*contracts.NetworkConfigSpec, error) {
	var spec contracts.NetworkConfigSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return nil, fmt.Errorf("networks: decode stored config: %w", err)
	}
	return &spec, nil
}

func hashOf(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
