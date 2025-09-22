package decypharr

import (
	"context"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/qbit"
	"github.com/sirrobot01/decypharr/pkg/server"
	"github.com/sirrobot01/decypharr/pkg/store"
	"github.com/sirrobot01/decypharr/pkg/version"
	"github.com/sirrobot01/decypharr/pkg/web"
	"github.com/sirrobot01/decypharr/pkg/webdav"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"sync"
	"time"
)

func Start(ctx context.Context) error {

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

	// Create the logger path if it doesn't exist
	for {
		cfg := config.Get()
		_log := logger.Default()

		// ascii banner
		fmt.Printf(`
+-------------------------------------------------------+
|                                                       |
|  ╔╦╗╔═╗╔═╗╦ ╦╔═╗╦ ╦╔═╗╦═╗╦═╗                          |
|   ║║║╣ ║  └┬┘╠═╝╠═╣╠═╣╠╦╝╠╦╝ (%s)        |
|  ═╩╝╚═╝╚═╝ ┴ ╩  ╩ ╩╩ ╩╩╚═╩╚═                          |
|                                                       |
+-------------------------------------------------------+
|  Log Level: %s                                        |
+-------------------------------------------------------+
`, version.GetInfo(), cfg.LogLevel)

		// Initialize services
		qb := qbit.New()
		wd := webdav.New()

		ui := web.New().Routes()
		webdavRoutes := wd.Routes()
		qbitRoutes := qb.Routes()

		// Register routes
		handlers := map[string]http.Handler{
			"/":       ui,
			"/api/v2": qbitRoutes,
			"/webdav": webdavRoutes,
		}
		srv := server.New(handlers)

		reset := func() {
			_log.Debug().Msg("Resetting services and store...")

			// Gracefully shutdown services with timeout
			resetCtx, resetCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer resetCancel()

			// Stop WebDAV handlers
			if err := wd.Stop(); err != nil {
				_log.Error().Err(err).Msg("Error stopping WebDAV service")
			}

			// Reset QBit service
			qb.Reset()

			// Shutdown store with proper coordination
			if err := store.Get().Shutdown(resetCtx); err != nil {
				_log.Error().Err(err).Msg("Error shutting down store")
			}

			// Reset store for clean restart
			store.Reset()

			// Force garbage collection to clean up resources
			runtime.GC()
			_log.Debug().Msg("Service reset completed")
		}

		done := make(chan struct{})
		go func(ctx context.Context) {
			if err := startServices(ctx, cancelSvc, wd, srv); err != nil {
				_log.Error().Err(err).Msg("Error starting services")
				cancelSvc()
			}
			close(done)
		}(svcCtx)

		select {
		case <-ctx.Done():
			// Graceful shutdown with timeout
			_log.Info().Msg("Initiating graceful shutdown...")
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer shutdownCancel()

			cancelSvc() // propagate to services

			// Wait for services to stop with timeout
			select {
			case <-done:
				_log.Info().Msg("All services stopped gracefully")
			case <-shutdownCtx.Done():
				_log.Warn().Msg("Graceful shutdown timeout reached, forcing shutdown")
			}

			_log.Info().Msg("Decypharr has been stopped gracefully.")
			reset() // reset store and services
			return nil

		case <-restartCh:
			// Graceful restart with timeout
			_log.Info().Msg("Initiating graceful restart...")
			restartCtx, restartCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer restartCancel()

			cancelSvc() // tell existing services to shut down

			// Wait for services to stop with timeout
			select {
			case <-done:
				_log.Info().Msg("Services stopped for restart")
			case <-restartCtx.Done():
				_log.Warn().Msg("Restart shutdown timeout reached, forcing restart")
			}

			_log.Info().Msg("Decypharr has been restarted.")
			reset() // reset store and services
			// rebuild svcCtx off the original parent
			svcCtx, cancelSvc = context.WithCancel(ctx)
		}
	}
}

func startServices(ctx context.Context, cancelSvc context.CancelFunc, wd *webdav.WebDav, srv *server.Server) error {
	var wg sync.WaitGroup
	errChan := make(chan error, 10) // Buffered to prevent blocking

	_log := logger.Default()

	// Service management with proper error handling and graceful shutdown
	safeGo := func(serviceName string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					stack := debug.Stack()
					_log.Error().
						Str("service", serviceName).
						Interface("panic", r).
						Str("stack", string(stack)).
						Msg("Recovered from panic in service")

					select {
					case errChan <- fmt.Errorf("service %s panic: %v", serviceName, r):
					default:
						// Channel full, log but don't block
						_log.Error().Str("service", serviceName).Msg("Error channel full, panic not reported")
					}
				}
			}()

			_log.Debug().Str("service", serviceName).Msg("Starting service")
			if err := f(); err != nil && ctx.Err() == nil {
				_log.Error().Err(err).Str("service", serviceName).Msg("Service error")
				select {
				case errChan <- fmt.Errorf("service %s error: %w", serviceName, err):
				default:
					// Channel full, log but don't block
					_log.Error().Str("service", serviceName).Msg("Error channel full, error not reported")
				}
			} else if ctx.Err() != nil {
				_log.Debug().Str("service", serviceName).Msg("Service stopped due to context cancellation")
			} else {
				_log.Debug().Str("service", serviceName).Msg("Service completed successfully")
			}
		}()
	}

	// Start core services with proper naming for better debugging
	safeGo("webdav", func() error {
		return wd.Start(ctx)
	})

	safeGo("http-server", func() error {
		return srv.Start(ctx)
	})

	// Start rclone RC server if enabled
	safeGo("rclone-manager", func() error {
		rcManager := store.Get().RcloneManager()
		if rcManager == nil {
			_log.Debug().Msg("Rclone manager not configured, skipping")
			return nil
		}
		return rcManager.Start(ctx)
	})

	// Start repair service if enabled
	if cfg := config.Get(); cfg.Repair.Enabled {
		safeGo("repair-service", func() error {
			repair := store.Get().Repair()
			if repair != nil {
				return repair.Start(ctx)
			}
			_log.Debug().Msg("Repair service not available")
			return nil
		})
	} else {
		_log.Debug().Msg("Repair service disabled in configuration")
	}

	// Start background workers
	safeGo("workers", func() error {
		store.Get().StartWorkers(ctx)
		return nil
	})

	// Monitor service completion
	go func() {
		wg.Wait()
		_log.Debug().Msg("All services have finished")
		close(errChan)
	}()

	// Handle service errors
	go func() {
		for err := range errChan {
			if err != nil {
				_log.Error().Err(err).Msg("Service error detected")
				// Only trigger shutdown if context is still active (not already shutting down)
				if ctx.Err() == nil {
					_log.Error().Msg("Initiating service shutdown due to critical error")
					cancelSvc() // Cancel the service context to stop all services
				}
			}
		}
	}()

	_log.Info().Msg("All services started successfully")

	// Wait for context cancellation (shutdown signal)
	<-ctx.Done()
	_log.Info().Msg("Shutdown signal received, stopping services...")

	// Give services a moment to stop gracefully
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		_log.Info().Msg("All services stopped gracefully")
	case <-shutdownCtx.Done():
		_log.Warn().Msg("Service shutdown timeout reached")
	}

	return ctx.Err()
}
