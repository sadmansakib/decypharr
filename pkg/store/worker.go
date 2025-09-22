package store

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// StartWorkers starts all background workers with proper context timeout and goroutine management
func (s *Store) StartWorkers(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Start debrid workers
	if err := s.Debrid().StartWorker(ctx); err != nil {
		s.logger.Error().Err(err).Msg("Failed to start debrid worker")
	} else {
		s.logger.Debug().Msg("Started debrid worker")
	}

	// Start cache workers with proper timeout and goroutine management
	s.startCacheWorkers(ctx)

	// Store queue workers
	if err := s.StartQueueWorkers(ctx); err != nil {
		s.logger.Error().Err(err).Msg("Failed to start store worker")
	} else {
		s.logger.Debug().Msg("Started store worker")
	}

	// Arr workers
	if err := s.Arr().StartWorker(ctx); err != nil {
		s.logger.Error().Err(err).Msg("Failed to start Arr worker")
	} else {
		s.logger.Debug().Msg("Started Arr worker")
	}
}

// startCacheWorkers starts cache workers with proper context timeout and goroutine lifecycle management
func (s *Store) startCacheWorkers(ctx context.Context) {
	caches := s.Debrid().Caches()
	if len(caches) == 0 {
		return
	}

	// Use WaitGroup to track goroutines
	var wg sync.WaitGroup
	workerTimeout := 30 * time.Second // Configurable timeout for worker startup

	for name, cache := range caches {
		if cache == nil {
			continue
		}

		wg.Add(1)
		go func(cacheName string, cache interface{}) {
			defer wg.Done()

			// Create a timeout context for this specific worker
			workerCtx, cancel := context.WithTimeout(ctx, workerTimeout)
			defer cancel()

			// Channel to capture the result of StartWorker
			done := make(chan error, 1)

			// Start the worker in a separate goroutine
			go func() {
				if c, ok := cache.(interface{ StartWorker(context.Context) error }); ok {
					done <- c.StartWorker(workerCtx)
				} else {
					done <- fmt.Errorf("cache does not implement StartWorker method")
				}
			}()

			// Wait for either completion or timeout/cancellation
			select {
			case err := <-done:
				if err != nil {
					s.logger.Error().Err(err).Msgf("Failed to start debrid cache worker for %s", cacheName)
				} else {
					s.logger.Debug().Msgf("Started debrid cache worker for %s", cacheName)
				}
			case <-workerCtx.Done():
				if workerCtx.Err() == context.DeadlineExceeded {
					s.logger.Warn().Msgf("Cache worker startup timed out after %v for %s", workerTimeout, cacheName)
				} else {
					s.logger.Debug().Msgf("Cache worker startup cancelled for %s", cacheName)
				}
			}
		}(name, cache)
	}

	// Start a goroutine to wait for all workers with overall timeout
	go func() {
		overallTimeout := 60 * time.Second
		overallCtx, cancel := context.WithTimeout(ctx, overallTimeout)
		defer cancel()

		// Channel to signal when all workers are done
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		// Wait for either all workers to complete or timeout
		select {
		case <-done:
			s.logger.Debug().Msg("All cache workers started successfully")
		case <-overallCtx.Done():
			if overallCtx.Err() == context.DeadlineExceeded {
				s.logger.Warn().Msgf("Overall cache worker startup timed out after %v", overallTimeout)
			} else {
				s.logger.Debug().Msg("Cache worker startup cancelled")
			}
		}
	}()
}
