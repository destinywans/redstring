package inventory

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"log/slog"
	"os"
	"sync"
	"time"
)

type Syncer struct {
	DB      *pgxpool.Pool
	Config  Config
	clients []*kubeClient
}

func New(db *pgxpool.Pool, cfg Config) (*Syncer, error) {
	s := &Syncer{DB: db, Config: cfg}
	seen := map[string]bool{}
	for _, source := range cfg.Sources {
		if len(source.GrantPermissionIDs) == 0 {
			source.GrantPermissionIDs = cfg.GrantPermissionIDs
		}
		if source.ID == "" || seen[source.ID] {
			return nil, fmt.Errorf("inventory source IDs must be unique")
		}
		seen[source.ID] = true
		k, err := newKube(source)
		if err != nil {
			return nil, err
		}
		s.clients = append(s.clients, k)
	}
	return s, nil
}
func delay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Run blocks until ctx cancellation. Launch once, after DB migrations. A source
// failure is persisted independently and never prevents other sources syncing.
func (s *Syncer) Run(ctx context.Context) {
	var wg sync.WaitGroup
	interval := time.Duration(s.Config.IntervalSeconds) * time.Second
	if interval < 10*time.Second {
		interval = 30 * time.Second
	}
	if interval > 5*time.Minute {
		interval = 5 * time.Minute
	}
	for _, client := range s.clients {
		wg.Add(1)
		go func(k *kubeClient) {
			defer wg.Done()
			for ctx.Err() == nil {
				started := time.Now()
				snapshot, versions, err := k.snapshot(ctx)
				if err != nil {
					snapshot = Snapshot{SourceID: k.source.ID, SourceKind: "kubernetes", SourceName: k.source.Name, Error: err.Error()}
				}
				if err == nil {
					enrichMetrics(ctx, &snapshot, s.Config.PrometheusURL)
				}
				if saveErr := SaveSnapshot(ctx, s.DB, snapshot); saveErr != nil {
					slog.Error("inventory save failed", "source", k.source.ID, "error", saveErr)
				}
				if err != nil {
					if !delay(ctx, interval) {
						return
					}
					continue
				}
				k.awaitChange(ctx, versions, interval)
				// Limit LIST traffic even under a continuous stream of Pod/status events.
				if remaining := 10*time.Second - time.Since(started); remaining > 0 && !delay(ctx, remaining) {
					return
				}
			}
		}(client)
	}
	if s.Config.ExternalSnapshotFile != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if err := s.importExternal(ctx); err != nil {
					slog.Error("external inventory import failed", "error", err)
				}
				if !delay(ctx, interval) {
					return
				}
			}
		}()
	}
	wg.Wait()
}
func (s *Syncer) importExternal(ctx context.Context) error {
	info, err := os.Stat(s.Config.ExternalSnapshotFile)
	if err != nil {
		return err
	}
	if info.Size() > 32<<20 {
		return fmt.Errorf("external snapshot too large")
	}
	raw, err := os.ReadFile(s.Config.ExternalSnapshotFile)
	if err != nil {
		return err
	}
	var file ExternalFile
	if err = json.Unmarshal(raw, &file); err != nil {
		return err
	}
	for _, snapshot := range file.Sources {
		snapshot.GrantPermissionIDs = s.Config.GrantPermissionIDs
		if snapshot.SourceKind != "docker" && snapshot.SourceKind != "lima" {
			return fmt.Errorf("unsupported external source kind")
		}
		if snapshot.Error == "" {
			enrichMetrics(ctx, &snapshot, s.Config.PrometheusURL)
		}
		if err = SaveSnapshot(ctx, s.DB, snapshot); err != nil {
			return err
		}
	}
	return nil
}
