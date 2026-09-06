package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"agent-platform/internal/contracts"
	"agent-platform/internal/images"
	"agent-platform/model"
)

func seedCommand(ctx context.Context, log *slog.Logger, store *model.Store) int {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	clientName := fs.String("client", "", "client name (required when creating)")
	clientID := fs.String("client-id", "", "existing client id to reuse")
	imageID := fs.String("image", "", "image_id (required)")
	manifestPath := fs.String("manifest", "", "path to the Runtime manifest JSON (required)")
	digest := fs.String("digest", "", "immutable image digest sha256:<64 hex> (required)")
	key := fs.String("key", "", "raw API key to register (else a random one is generated)")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return 2
	}
	if *imageID == "" || *manifestPath == "" || *digest == "" {
		fmt.Fprintln(os.Stderr, "seed requires --image, --manifest, --digest")
		return 2
	}
	if *clientID == "" && *clientName == "" {
		fmt.Fprintln(os.Stderr, "seed requires --client or --client-id")
		return 2
	}
	if err := contracts.ValidateImageDigest(*digest); err != nil {
		fmt.Fprintf(os.Stderr, "invalid digest: %v\n", err)
		return 2
	}
	manifestBytes, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read manifest: %v\n", err)
		return 2
	}
	manifest, verr := contracts.DecodeManifest(manifestBytes)
	if verr != nil {
		fmt.Fprintf(os.Stderr, "decode manifest: %v\n", verr)
		return 2
	}

	cid := *clientID
	if cid == "" {
		c := &model.Client{Name: *clientName}
		if err := store.DAOs().Clients.Create(ctx, c); err != nil {
			fmt.Fprintf(os.Stderr, "create client: %v\n", err)
			return 1
		}
		cid = c.ID
	} else {
		if _, err := store.DAOs().Clients.Get(ctx, cid); err != nil {
			fmt.Fprintf(os.Stderr, "client not found: %v\n", err)
			return 1
		}
	}

	rawKey := *key
	if rawKey == "" {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			fmt.Fprintf(os.Stderr, "random: %v\n", err)
			return 1
		}
		rawKey = base64.RawURLEncoding.EncodeToString(raw)
	}
	sum := sha256.Sum256([]byte(rawKey))
	if err := store.DAOs().APIKeys.Create(ctx, &model.APIKey{
		ClientID: cid, KeyHash: hex.EncodeToString(sum[:]), Label: "seed", Active: true,
	}); err != nil {
		if !model.IsDuplicate(err) {
			fmt.Fprintf(os.Stderr, "create api key: %v\n", err)
			return 1
		}
	}

	imgSvc := images.New(store, devVerifier{}, log)
	reg, _, err := imgSvc.Register(ctx, images.RegisterInput{
		ClientID: cid, ImageID: *imageID, Digest: *digest, Manifest: manifest,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "register image: %v\n", err)
		return 1
	}
	if _, err := imgSvc.Enable(ctx, cid, *imageID, images.Evidence{
		Digest: *digest, ValidationRef: "seed-" + reg.ID,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "enable image: %v\n", err)
		return 1
	}

	fmt.Printf("client_id=%s\n", cid)
	fmt.Printf("image_id=%s\n", *imageID)
	fmt.Printf("api_key=%s\n", rawKey)
	return 0
}

type devVerifier struct{}

func (devVerifier) Verify(context.Context, string) error { return nil }
