package wire

import (
	"cmp"
	"context"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid"
	"github.com/sirrobot01/decypharr/pkg/rclone"
	"github.com/sirrobot01/decypharr/pkg/repair"
)

type Store struct {
	repair             *repair.Repair
	arr                *arr.Storage
	debrid             *debrid.Storage
	rcloneManager      *rclone.Manager
	importsQueue       *ImportQueue // Queued import requests(probably from too_many_active_downloads)
	torrents           *TorrentStorage
	logger             zerolog.Logger
	refreshInterval    time.Duration
	skipPreCache       bool
	downloadSemaphore  chan struct{}
	removeStalledAfter time.Duration // Duration after which stalled torrents are removed
	scheduler          gocron.Scheduler
}

var (
	instance *Store
	once     sync.Once
)

// Get returns the singleton instance
func Get() *Store {
	once.Do(func() {
		cfg := config.Get()
		qbitCfg := cfg.QBitTorrent

		// Create rclone manager if enabled
		var rcManager *rclone.Manager
		if cfg.Rclone.Enabled {
			rcManager = rclone.NewManager()
		}

		// Create services with dependencies
		arrs := arr.NewStorage()
		deb := debrid.NewStorage(rcManager)

		scheduler, err := gocron.NewScheduler(gocron.WithLocation(time.Local), gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-store")))
		if err != nil {
			scheduler, _ = gocron.NewScheduler(gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-store")))
		}

		defaultLogger := logger.Default() // Use default logger [decypharr]
		instance = &Store{
			repair:            repair.New(arrs, deb),
			arr:               arrs,
			debrid:            deb,
			rcloneManager:     rcManager,
			torrents:          newTorrentStorage(cfg.TorrentsFile(), defaultLogger),
			logger:            defaultLogger,
			refreshInterval:   time.Duration(cmp.Or(qbitCfg.RefreshInterval, 30)) * time.Second,
			skipPreCache:      qbitCfg.SkipPreCache,
			downloadSemaphore: make(chan struct{}, cmp.Or(qbitCfg.MaxDownloads, 5)),
			importsQueue:      NewImportQueue(context.Background(), 1000),
			scheduler:         scheduler,
		}
		if cfg.RemoveStalledAfter != "" {
			removeStalledAfter, err := time.ParseDuration(cfg.RemoveStalledAfter)
			if err == nil {
				instance.removeStalledAfter = removeStalledAfter
			}
		}
	})
	return instance
}

func Reset() {
	if instance != nil {
		if instance.debrid != nil {
			instance.debrid.Reset()
		}

		if instance.rcloneManager != nil {
			err := instance.rcloneManager.Stop()
			if err != nil {
				instance.logger.Error().Err(err).Msg("Failed to stop rclone manager")
			}
		}

		if instance.importsQueue != nil {
			instance.importsQueue.Close()
		}
		if instance.downloadSemaphore != nil {
			// Close the semaphore channel to
			close(instance.downloadSemaphore)
		}

		if instance.scheduler != nil {
			_ = instance.scheduler.StopJobs()
			_ = instance.scheduler.Shutdown()
		}
	}
	once = sync.Once{}
	instance = nil
}

func (s *Store) Arr() *arr.Storage {
	return s.arr
}
func (s *Store) Debrid() *debrid.Storage {
	return s.debrid
}
func (s *Store) Repair() *repair.Repair {
	return s.repair
}
func (s *Store) Torrents() *TorrentStorage {
	return s.torrents
}
func (s *Store) RcloneManager() *rclone.Manager {
	return s.rcloneManager
}

func (s *Store) Scheduler() gocron.Scheduler {
	return s.scheduler
}
