package store

import (
	"cmp"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid"
	"github.com/sirrobot01/decypharr/pkg/rclone"
	"github.com/sirrobot01/decypharr/pkg/repair"
)

// ServiceFactory creates and configures services with proper dependency injection
type ServiceFactory struct {
	config *config.Config
}

// NewServiceFactory creates a new service factory
func NewServiceFactory(cfg *config.Config) *ServiceFactory {
	return &ServiceFactory{config: cfg}
}

// CreateServices creates all services with proper dependency injection
func (sf *ServiceFactory) CreateServices() (*Store, error) {
	cfg := sf.config
	qbitCfg := cfg.QBitTorrent

	// Parse durations
	refreshInterval := time.Duration(cmp.Or(qbitCfg.RefreshInterval, 30)) * time.Second
	maxDownloads := cmp.Or(qbitCfg.MaxDownloads, 5)

	var removeStalledAfter time.Duration
	if cfg.RemoveStalledAfter != "" {
		if duration, err := time.ParseDuration(cfg.RemoveStalledAfter); err == nil {
			removeStalledAfter = duration
		}
	}

	// Create Store with configuration
	storeConfig := StoreConfig{
		TorrentsFile:       cfg.TorrentsFile(),
		Logger:             logger.Default(),
		RefreshInterval:    refreshInterval,
		SkipPreCache:       qbitCfg.SkipPreCache,
		MaxDownloads:       maxDownloads,
		RemoveStalledAfter: removeStalledAfter,
	}

	store, err := NewStore(storeConfig)
	if err != nil {
		return nil, err
	}

	// Create services
	arrService := sf.createArrService()
	rcloneManager := sf.createRcloneManager()
	debridService := sf.createDebridService(rcloneManager)
	repairService := sf.createRepairService(arrService, debridService)

	// Inject services into store
	store.InjectServices(arrService, debridService, repairService, rcloneManager)

	return store, nil
}

// createArrService creates the Arr service
func (sf *ServiceFactory) createArrService() *arr.Storage {
	return arr.NewStorage()
}

// createRcloneManager creates the Rclone manager if enabled
func (sf *ServiceFactory) createRcloneManager() *rclone.Manager {
	if sf.config.Rclone.Enabled {
		return rclone.NewManager()
	}
	return nil
}

// createDebridService creates the Debrid service
func (sf *ServiceFactory) createDebridService(rcManager *rclone.Manager) *debrid.Storage {
	return debrid.NewStorage(rcManager)
}

// createRepairService creates the Repair service
func (sf *ServiceFactory) createRepairService(arrService *arr.Storage, debridService *debrid.Storage) *repair.Repair {
	return repair.New(arrService, debridService)
}

// CreateStoreFromConfig is a convenience function to create a Store from configuration
// This maintains backward compatibility while enabling the new dependency injection pattern
func CreateStoreFromConfig(cfg *config.Config) (*Store, error) {
	factory := NewServiceFactory(cfg)
	return factory.CreateServices()
}

// CreateStoreWithDependencies creates a Store with explicit dependencies
// This is useful for testing or when you want to provide your own service implementations
func CreateStoreWithDependencies(
	storeConfig StoreConfig,
	arrService *arr.Storage,
	debridService *debrid.Storage,
	repairService *repair.Repair,
	rcloneManager *rclone.Manager,
) (*Store, error) {
	store, err := NewStore(storeConfig)
	if err != nil {
		return nil, err
	}

	store.InjectServices(arrService, debridService, repairService, rcloneManager)
	return store, nil
}

// CreateStoreForTesting creates a Store configured for testing
func CreateStoreForTesting() (*Store, error) {
	storeConfig := StoreConfig{
		TorrentsFile:       ":memory:",
		Logger:             logger.New("test"),
		RefreshInterval:    30 * time.Second,
		SkipPreCache:       true,
		MaxDownloads:       5,
		RemoveStalledAfter: 0,
	}

	store, err := NewStore(storeConfig)
	if err != nil {
		return nil, err
	}

	// Create minimal services for testing
	arrService := arr.NewStorage()
	debridService := debrid.NewStorage(nil)
	repairService := repair.New(arrService, debridService)

	store.InjectServices(arrService, debridService, repairService, nil)
	return store, nil
}