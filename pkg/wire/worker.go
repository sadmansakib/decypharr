package wire

import (
	"context"
	"sync"
	"time"
)

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

	// Cache workers - start with proper goroutine lifecycle management
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

// startCacheWorkers starts cache workers with proper context handling and lifecycle management
func (s *Store) startCacheWorkers(ctx context.Context) {
	caches := s.Debrid().Caches()
	if len(caches) == 0 {
		return
	}

	// Use a WaitGroup to track cache worker goroutines
	var wg sync.WaitGroup
	cacheCtx, cancel := context.WithCancel(ctx)

	// Store the cancel function for graceful shutdown
	s.cacheWorkersCancel = cancel

	for _, cache := range caches {
		if cache == nil {
			continue
		}

		// Capture the cache variable properly to avoid race conditions
		cacheWorker := cache
		wg.Add(1)

		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					s.logger.Error().Interface("panic", r).Msgf("Cache worker panic recovered for %s", cacheWorker.GetConfig().Name)
				}
			}()

			if err := cacheWorker.StartWorker(cacheCtx); err != nil {
				// Only log error if it's not due to context cancellation
				if cacheCtx.Err() == nil {
					s.logger.Error().Err(err).Msgf("Failed to start debrid cache worker for %s", cacheWorker.GetConfig().Name)
				}
			} else {
				s.logger.Debug().Msgf("Started debrid cache worker for %s", cacheWorker.GetConfig().Name)
			}
		}()
	}

	// Start a goroutine to handle graceful shutdown of cache workers
	go func() {
		<-ctx.Done()
		s.logger.Debug().Msg("Shutting down cache workers...")
		cancel() // Cancel the cache worker contexts

		// Wait for all cache workers to finish with a timeout
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			s.logger.Debug().Msg("All cache workers shut down gracefully")
		case <-time.After(10 * time.Second):
			s.logger.Warn().Msg("Cache workers shutdown timeout reached")
		}
	}()
}

// StopWorkers provides graceful shutdown for all workers
func (s *Store) StopWorkers() {
	s.logger.Info().Msg("Stopping all workers...")

	// Cancel cache workers if they were started
	if s.cacheWorkersCancel != nil {
		s.cacheWorkersCancel()
	}

	s.logger.Debug().Msg("All workers stop signal sent")
}
