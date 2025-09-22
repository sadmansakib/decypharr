package decypharr

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/qbit"
	"github.com/sirrobot01/decypharr/pkg/server"
	"github.com/sirrobot01/decypharr/pkg/store"
	"github.com/sirrobot01/decypharr/pkg/version"
	"github.com/sirrobot01/decypharr/pkg/web"
	"github.com/sirrobot01/decypharr/pkg/webdav"
)

// AppServices holds all application services with dependency injection
type AppServices struct {
	store  *store.Store
	qbit   *qbit.QBit
	webdav *webdav.WebDav
	web    *web.Web
	server *server.Server
	logger zerolog.Logger
	config *config.Config
}

// NewAppServices creates a new application services container with dependency injection
func NewAppServices(cfg *config.Config) (*AppServices, error) {
	_log := logger.Default()

	// Create store using the new factory pattern
	storeInstance, err := store.CreateStoreFromConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	// Create services with store dependency injection
	qbitService := qbit.NewWithStore(storeInstance)
	webService := web.NewWithStore(storeInstance)
	webdavService := webdav.NewWithStore(storeInstance)

	// Create routes
	ui := webService.Routes()
	webdavRoutes := webdavService.Routes()
	qbitRoutes := qbitService.Routes()

	// Register routes
	handlers := map[string]http.Handler{
		"/":       ui,
		"/api/v2": qbitRoutes,
		"/webdav": webdavRoutes,
	}
	serverInstance := server.New(handlers)

	return &AppServices{
		store:  storeInstance,
		qbit:   qbitService,
		webdav: webdavService,
		web:    webService,
		server: serverInstance,
		logger: _log,
		config: cfg,
	}, nil
}

// StartWithDI starts the application using dependency injection pattern
func StartWithDI(ctx context.Context) error {
	if umaskStr := os.Getenv("UMASK"); umaskStr != "" {
		umask, err := strconv.ParseInt(umaskStr, 8, 32)
		if err != nil {
			return fmt.Errorf("invalid UMASK value: %s", umaskStr)
		}
		SetUmask(int(umask))
	}

	restartCh := make(chan struct{}, 1)
	web.SetRestartFunc(func() {
		select {
		case restartCh <- struct{}{}:
		default:
		}
	})

	svcCtx, cancelSvc := context.WithCancel(ctx)
	defer cancelSvc()

	for {
		cfg := config.Get()
		_log := logger.Default()

		// ASCII banner
		fmt.Printf(`
+-------------------------------------------------------+
|                                                       |
|  ╔╦╗╔═╗╔═╗╦ ╦╔═╗╦ ╦╔═╗╦═╗╦═╗                          |
|   ║║║╣ ║  └┬┘╠═╝╠═╣╠═╣╠╦╝╠╦╝ (%s)        |
|  ═╩╝╚═╝╚═╝ ┴ ╩  ╩ ╩╩ ╩╩╚═╩╚═                          |
|                                                       |
+-------------------------------------------------------+
|  Log Level: %s                                        |
|  Architecture: Dependency Injection                  |
+-------------------------------------------------------+
`, version.GetInfo(), cfg.LogLevel)

		// Create application services with dependency injection
		appServices, err := NewAppServices(cfg)
		if err != nil {
			return fmt.Errorf("failed to create application services: %w", err)
		}

		done := make(chan struct{})
		go func(ctx context.Context) {
			if err := startServicesWithDI(ctx, cancelSvc, appServices); err != nil {
				_log.Error().Err(err).Msg("Error starting services")
				cancelSvc()
			}
			close(done)
		}(svcCtx)

		select {
		case <-ctx.Done():
			// graceful shutdown
			cancelSvc() // propagate to services
			<-done      // wait for them to finish
			_log.Info().Msg("Decypharr has been stopped gracefully.")
			appServices.Reset() // reset services
			return nil

		case <-restartCh:
			cancelSvc() // tell existing services to shut down
			_log.Info().Msg("Restarting Decypharr...")
			<-done // wait for them to finish
			_log.Info().Msg("Decypharr has been restarted.")
			appServices.Reset() // reset services
			// rebuild svcCtx off the original parent
			svcCtx, cancelSvc = context.WithCancel(ctx)
		}
	}
}

func startServicesWithDI(ctx context.Context, cancelSvc context.CancelFunc, appServices *AppServices) error {
	var wg sync.WaitGroup
	errChan := make(chan error)

	_log := appServices.logger

	safeGo := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					stack := debug.Stack()
					_log.Error().
						Interface("panic", r).
						Str("stack", string(stack)).
						Msg("Recovered from panic in goroutine")

					// Send error to channel so the main goroutine is aware
					errChan <- fmt.Errorf("panic: %v", r)
				}
			}()

			if err := f(); err != nil {
				errChan <- err
			}
		}()
	}

	safeGo(func() error {
		return appServices.webdav.Start(ctx)
	})

	safeGo(func() error {
		return appServices.server.Start(ctx)
	})

	// Start rclone RC server if enabled
	safeGo(func() error {
		if rcManager := appServices.store.RcloneManager(); rcManager != nil {
			return rcManager.Start(ctx)
		}
		return nil
	})

	if appServices.config.Repair.Enabled {
		safeGo(func() error {
			if repair := appServices.store.Repair(); repair != nil {
				if err := repair.Start(ctx); err != nil {
					_log.Error().Err(err).Msg("repair failed")
				}
			}
			return nil
		})
	}

	safeGo(func() error {
		appServices.store.StartWorkers(ctx)
		return nil
	})

	go func() {
		wg.Wait()
		close(errChan)
	}()

	go func() {
		for err := range errChan {
			if err != nil {
				_log.Error().Err(err).Msg("Service error detected")
				// If the error is critical, return it to stop the main loop
				if ctx.Err() == nil {
					_log.Error().Msg("Stopping services due to error")
					cancelSvc() // Cancel the service context to stop all services
				}
			}
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	_log.Debug().Msg("Services context cancelled")
	return nil
}

// Reset cleans up application services
func (as *AppServices) Reset() {
	if as.qbit != nil {
		as.qbit.Reset()
	}
	if as.store != nil {
		as.store.Reset()
	}
	// refresh GC
	runtime.GC()
}

// GetStore returns the store instance (for backward compatibility)
func (as *AppServices) GetStore() *store.Store {
	return as.store
}
