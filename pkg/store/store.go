package store

import (
	"cmp"
	"context"
	"fmt"
	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid"
	"github.com/sirrobot01/decypharr/pkg/rclone"
	"github.com/sirrobot01/decypharr/pkg/repair"
	"sync"
	"time"
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
			// Fallback to scheduler without timezone location
			var fallbackErr error
			scheduler, fallbackErr = gocron.NewScheduler(gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-store")))
			if fallbackErr != nil {
				panic(fmt.Errorf("failed to create scheduler with timezone (%w) and fallback scheduler failed (%w)", err, fallbackErr))
			}
		}

		instance = &Store{
			repair:            repair.New(arrs, deb),
			arr:               arrs,
			debrid:            deb,
			rcloneManager:     rcManager,
			torrents:          newTorrentStorage(cfg.TorrentsFile()),
			logger:            logger.Default(), // Use default logger [decypharr]
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
			instance.rcloneManager.Stop()
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

// NewStore creates a new Store instance with dependency injection
// This replaces the singleton pattern for better testability and flexibility
func NewStore(config StoreConfig) (*Store, error) {
	// Create scheduler
	scheduler, err := gocron.NewScheduler(
		gocron.WithLocation(time.Local),
		gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-store")),
	)
	if err != nil {
		scheduler, _ = gocron.NewScheduler(gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-store")))
	}

	store := &Store{
		torrents:          newTorrentStorage(config.TorrentsFile),
		logger:            config.Logger,
		refreshInterval:   config.RefreshInterval,
		skipPreCache:      config.SkipPreCache,
		downloadSemaphore: make(chan struct{}, config.MaxDownloads),
		importsQueue:      NewImportQueue(context.Background(), 1000),
		scheduler:         scheduler,
		removeStalledAfter: config.RemoveStalledAfter,
	}

	return store, nil
}

// StoreConfig holds configuration for creating a new Store
type StoreConfig struct {
	TorrentsFile       string
	Logger             zerolog.Logger
	RefreshInterval    time.Duration
	SkipPreCache       bool
	MaxDownloads       int
	RemoveStalledAfter time.Duration
}

// InjectServices allows injecting dependencies after Store creation
func (s *Store) InjectServices(
	arrService *arr.Storage,
	debridService *debrid.Storage,
	repairService *repair.Repair,
	rcloneManager *rclone.Manager,
) {
	s.arr = arrService
	s.debrid = debridService
	s.repair = repairService
	s.rcloneManager = rcloneManager
}

// GetDownloadSemaphore returns the download semaphore (for backward compatibility)
func (s *Store) GetDownloadSemaphore() chan struct{} {
	return s.downloadSemaphore
}

// GetRefreshInterval returns the refresh interval (for backward compatibility)
func (s *Store) GetRefreshInterval() time.Duration {
	return s.refreshInterval
}

// GetSkipPreCache returns the skip pre-cache setting (for backward compatibility)
func (s *Store) GetSkipPreCache() bool {
	return s.skipPreCache
}

// GetRemoveStalledAfter returns the remove stalled after duration (for backward compatibility)
func (s *Store) GetRemoveStalledAfter() time.Duration {
	return s.removeStalledAfter
}

// Reset cleans up the store (for backward compatibility with existing Reset function)
func (s *Store) Reset() {
	// Use the new Shutdown method for coordinated cleanup
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.Shutdown(ctx); err != nil {
		s.logger.Error().Err(err).Msg("Error during store reset")
	}
}

// GetMaxDownloads returns the maximum number of downloads (for testing)
func (s *Store) GetMaxDownloads() int {
	return cap(s.downloadSemaphore)
}

// Shutdown gracefully shuts down all store services with proper coordination
func (s *Store) Shutdown(ctx context.Context) error {
	s.logger.Info().Msg("Initiating store shutdown...")

	// Stop repair service first
	if s.repair != nil {
		s.logger.Debug().Msg("Stopping repair service...")
		if err := s.repair.Stop(ctx); err != nil {
			s.logger.Error().Err(err).Msg("Error stopping repair service")
		}
	}

	// Stop debrid services
	if s.debrid != nil {
		s.logger.Debug().Msg("Stopping debrid services...")
		s.debrid.Reset()
	}

	// Stop rclone manager
	if s.rcloneManager != nil {
		s.logger.Debug().Msg("Stopping rclone manager...")
		if err := s.rcloneManager.Stop(); err != nil {
			s.logger.Error().Err(err).Msg("Error stopping rclone manager")
		}
	}

	// Close imports queue
	if s.importsQueue != nil {
		s.logger.Debug().Msg("Closing imports queue...")
		s.importsQueue.Close()
	}

	// Stop scheduler
	if s.scheduler != nil {
		s.logger.Debug().Msg("Stopping scheduler...")
		if err := s.scheduler.StopJobs(); err != nil {
			s.logger.Error().Err(err).Msg("Error stopping scheduler jobs")
		}
		if err := s.scheduler.Shutdown(); err != nil {
			s.logger.Error().Err(err).Msg("Error shutting down scheduler")
		}
	}

	s.logger.Info().Msg("Store shutdown completed")
	return nil
}
