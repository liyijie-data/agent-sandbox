package reconciler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"agent-platform/internal/config"
	"agent-platform/model"
)

type BaselineSpec struct {
	PodCIDRs             []string `json:"pod_cidrs"`
	ServiceCIDRs         []string `json:"service_cidrs"`
	NodeCIDRs            []string `json:"node_cidrs"`
	ProtectedCIDRs       []string `json:"protected_cidrs"`
	BusinessPrivateCIDRs []string `json:"business_private_cidrs"`
	ReservedHostnames    []string `json:"reserved_hostnames"`
	IPv6Enabled          bool     `json:"ipv6_enabled"`
	RegistryEndpoint     string   `json:"registry_endpoint"`
	ObjectStorageCIDR    string   `json:"object_storage_cidr"`
	ObjectStoragePort    int      `json:"object_storage_port"`
}

func baselineFacts(cfg config.Config) BaselineSpec {
	return BaselineSpec{
		PodCIDRs:             splitList(cfg.Security.PodCIDRs),
		ServiceCIDRs:         splitList(cfg.Security.ServiceCIDRs),
		NodeCIDRs:            splitList(cfg.Security.NodeCIDRs),
		ProtectedCIDRs:       splitList(cfg.Security.ProtectedCIDRs + "," + cfg.Security.ReservedCIDRs),
		BusinessPrivateCIDRs: splitList(cfg.Security.BusinessPrivateCIDRs),
		ReservedHostnames:    splitList(cfg.Security.ReservedHostnames),
		IPv6Enabled:          cfg.Security.IPv6Enabled,
		RegistryEndpoint:     cfg.Security.RegistryEndpoint,
		ObjectStorageCIDR:    cfg.K8s.ObjectStorageCIDR,
		ObjectStoragePort:    cfg.K8s.ObjectStoragePort,
	}
}

func canonicalBaseline(cfg config.Config) ([]byte, []byte, error) {
	raw, err := json.Marshal(baselineFacts(cfg))
	if err != nil {
		return nil, nil, fmt.Errorf("reconciler: marshal baseline: %w", err)
	}
	sum := sha256.Sum256(raw)
	return raw, sum[:], nil
}

func EnsureBaseline(ctx context.Context, store *model.Store, cfg config.Config, log *slog.Logger) error {
	d := store.DAOs()
	cfgJSON, hash, err := canonicalBaseline(cfg)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		rows, err := d.PlatformNetwork.List(ctx)
		if err != nil {
			return err
		}
		for i := range rows {
			if bytes.Equal(rows[i].ConfigHash, hash) {
				if err := activateBaseline(ctx, d, &rows[i]); err != nil {
					return err
				}

				return ensurePlatformRolloutJob(ctx, d, &rows[i])
			}
		}
		rev := int64(1)
		for _, r := range rows {
			if r.Revision >= rev {
				rev = r.Revision + 1
			}
		}
		row := &model.PlatformNetworkBaseline{Revision: rev, ConfigHash: hash, Config: string(cfgJSON)}
		err = d.PlatformNetwork.Create(ctx, row)
		if err != nil {
			if model.IsDuplicate(err) {
				continue
			}
			return err
		}
		if log != nil {
			log.Info("reconciler: platform baseline persisted", "revision", rev, "config_hash", fmt.Sprintf("%x", hash))
		}
		if err := activateBaseline(ctx, d, row); err != nil {
			return err
		}

		return ensurePlatformRolloutJob(ctx, d, row)
	}
	return fmt.Errorf("reconciler: ensure baseline: concurrent seed contention")
}

func ensurePlatformRolloutJob(ctx context.Context, d model.DAOs, row *model.PlatformNetworkBaseline) error {
	requestID := fmt.Sprintf("platform-baseline-%d", row.Revision)
	if _, err := d.RolloutJobs.GetByRequest(ctx, "platform", requestID, "", ""); err == nil {
		return nil
	} else if !errors.Is(err, model.ErrNotFound) {
		return err
	}
	job := &model.RolloutJob{Scope: "platform", RequestID: requestID,
		PlatformRevisionID: &row.ID, Status: "pending"}
	if err := d.RolloutJobs.Create(ctx, job); err != nil {
		if model.IsDuplicate(err) {
			return nil
		}
		return err
	}
	return nil
}

func activateBaseline(ctx context.Context, d model.DAOs, row *model.PlatformNetworkBaseline) error {
	head, err := d.PlatformNetwork.GetHead(ctx)
	if err != nil {
		return err
	}
	if head.ActiveRevisionID != nil && *head.ActiveRevisionID == row.ID {
		return nil
	}
	if ok, err := d.PlatformNetwork.SetDesired(ctx, row.ID, head.ActiveRevisionID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("reconciler: activate baseline %s: desired CAS lost", row.ID)
	}
	ok, err := d.PlatformNetwork.Promote(ctx, head.ActiveRevisionID, &row.ID)
	if err != nil {
		return err
	}
	if !ok {

		again, gerr := d.PlatformNetwork.GetHead(ctx)
		if gerr != nil {
			return gerr
		}
		if again.ActiveRevisionID == nil || *again.ActiveRevisionID != row.ID {
			return fmt.Errorf("reconciler: activate baseline %s: promotion CAS lost", row.ID)
		}
	}
	return nil
}

func splitList(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if v := strings.TrimSpace(s); v != "" {
			out = append(out, v)
		}
	}
	return out
}
