package model

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const SchemaVersion = 4

//go:embed schema.sql
var schemaSQL string

type Store struct {
	db *gorm.DB
}

func newStore(db *gorm.DB) *Store { return &Store{db: db} }

func Open(ctx context.Context, dsn string) (*Store, error) {

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("model: connect: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("model: pool access: %w", err)
	}

	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("model: ping: %w", err)
	}
	return newStore(db), nil
}

type DAOs struct {
	Clients         ClientDAO
	APIKeys         APIKeyDAO
	Images          ImageDAO
	Runs            RunDAO
	Stages          RunStageDAO
	Contents        RunContentDAO
	Checkpoints     CheckpointDAO
	Inputs          RunInputDAO
	Outbox          OutboxDAO
	Steers          RunSteerDAO
	Batches         SteerBatchDAO
	CleanupJobs     CleanupJobDAO
	NetworkConfigs  NetworkConfigRevisionDAO
	NetworkHeads    NetworkConfigHeadDAO
	PlatformNetwork PlatformNetworkDAO
	RuntimeProfiles RuntimeProfileDAO
	RolloutJobs     RolloutJobDAO
	Platform        PlatformSettingsDAO
}

func daos(db *gorm.DB) DAOs {
	return DAOs{
		Clients: ClientDAO{db}, APIKeys: APIKeyDAO{db}, Images: ImageDAO{db},
		Runs: RunDAO{db}, Stages: RunStageDAO{db}, Contents: RunContentDAO{db},
		Checkpoints: CheckpointDAO{db}, Inputs: RunInputDAO{db}, Outbox: OutboxDAO{db},
		Steers: RunSteerDAO{db}, Batches: SteerBatchDAO{db}, CleanupJobs: CleanupJobDAO{db},
		NetworkConfigs: NetworkConfigRevisionDAO{db}, NetworkHeads: NetworkConfigHeadDAO{db}, PlatformNetwork: PlatformNetworkDAO{db}, RuntimeProfiles: RuntimeProfileDAO{db}, RolloutJobs: RolloutJobDAO{db}, Platform: PlatformSettingsDAO{db},
	}
}

func (s *Store) DAOs() DAOs { return daos(s.db) }

func (s *Store) Transaction(ctx context.Context, fn func(DAOs) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return fn(daos(tx)) })
}

func (s *Store) Close() {
	if s.db == nil {
		return
	}
	if sqlDB, err := s.db.DB(); err == nil && sqlDB != nil {
		_ = sqlDB.Close()
	}
}

var errUnknownSchema = &SchemaMismatchError{Msg: "database is not empty and has no schema_migrations marker; refusing to initialize a legacy/unknown schema"}

type SchemaMismatchError struct{ Msg string }

func (e *SchemaMismatchError) Error() string { return "model: " + e.Msg }

func SchemaMismatch(format string, args ...any) error {
	return &SchemaMismatchError{Msg: fmt.Sprintf(format, args...)}
}

func IsSchemaMismatch(err error) bool {
	_, ok := err.(*SchemaMismatchError)
	return ok
}

func (s *Store) Init(ctx context.Context) error {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {

		if err := tx.Exec(`SELECT pg_advisory_xact_lock(?)`, int64(0x41504C4154464F52)).Error; err != nil {
			return fmt.Errorf("model: acquire schema initialization lock: %w", err)
		}

		var marker *string
		if err := tx.Raw(`SELECT to_regclass('public.schema_migrations')::text`).Scan(&marker).Error; err != nil {
			return fmt.Errorf("model: init probe schema_migrations: %w", err)
		}
		if marker == nil {

			var n int64
			if err := tx.Raw(`SELECT count(*) FROM information_schema.tables
				WHERE table_schema='public' AND table_type='BASE TABLE'`).Scan(&n).Error; err != nil {
				return fmt.Errorf("model: init count public tables: %w", err)
			}
			if n != 0 {
				return errUnknownSchema
			}
			if err := tx.Exec(schemaSQL).Error; err != nil {
				return fmt.Errorf("model: apply schema: %w", err)
			}
			if err := tx.Exec(`INSERT INTO schema_migrations(version) VALUES (?)`, SchemaVersion).Error; err != nil {
				return fmt.Errorf("model: record version: %w", err)
			}
			return nil
		}
		var v int
		if err := tx.Raw(`SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`).Scan(&v).Error; err != nil {
			return fmt.Errorf("model: init read schema version: %w", err)
		}
		if v != SchemaVersion {
			return SchemaMismatch("database schema version %d does not match required %d", v, SchemaVersion)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}
