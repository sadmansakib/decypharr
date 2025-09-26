# Decypharr Improvement Tasks

Generated from codebase analysis on 2025-09-26

## 🚨 Critical Priority (Immediate - Next Sprint)

### Race Conditions & Concurrency Safety

- [x] **Task 1: Fix torrent storage race condition** (`pkg/wire/torrent_storage.go:53-70`)
  - Replace concurrent goroutines writing to file with single-writer pattern
  - Implement dedicated writer goroutine with channel-based communication
  - Add proper synchronization for file operations

- [x] **Task 2: Fix unsafe map access in Torbox provider** (`pkg/debrid/providers/torbox/torbox.go:501-544`)
  - Add mutex protection for concurrent map writes in `GetFileDownloadLinks`
  - Consider using sync.Map for high-concurrency scenarios
  - Review all debrid providers for similar issues
  - **COMPLETED**: Fixed race conditions in Torbox and AllDebrid providers using mutex-protected map writes

- [ ] **Task 3: Audit circuit breaker state management** (`internal/request/request.go:102-135`)
  - Review complex state transitions for race windows
  - Add comprehensive concurrency tests
  - Ensure atomic operations cover all state changes

### Resource Management & Memory Leaks

- [ ] **Task 4: Prevent goroutine leaks in service management** (`cmd/decypharr/main.go:153-192`)
  - Implement proper goroutine lifecycle management
  - Add context-based cancellation for all services
  - Ensure clean shutdown for error channel goroutines

- [ ] **Task 5: Implement bounded goroutine creation**
  - Add worker pools for file operations (`pkg/wire/torrent_storage.go`)
  - Limit concurrent Discord notifications (`pkg/repair/repair.go`)
  - Add semaphore-based concurrency control for cleanup operations

- [ ] **Task 6: Fix file handle leaks** (`internal/request/request.go:543-547`)
  - Ensure response bodies are closed in all error paths
  - Add defer statements immediately after successful response acquisition
  - Implement resource tracking for debugging

### Security Issues

- [ ] **Task 7: Remove hardcoded secret key** (`internal/config/config.go:338-340`)
  - Generate cryptographically secure random secret on first run
  - Store in secure configuration location
  - Add warning if default key is detected

- [ ] **Task 8: Implement input validation**
  - Add validation for all API endpoints
  - Sanitize file paths in WebDAV implementation
  - Validate URLs and torrent data inputs

- [ ] **Task 9: Add path traversal protection** (`pkg/webdav/`)
  - Implement proper path sanitization
  - Add bounds checking for file access
  - Audit all file operations for security

## ⚠️ High Priority (1-2 Sprints)

### Testing Infrastructure

- [ ] **Task 10: Set up testing framework**
  - Add testify and gomock dependencies
  - Create test utilities and helpers
  - Set up CI/CD test pipeline

- [ ] **Task 11: Implement critical path unit tests**
  - Test torrent processing logic (`pkg/wire/`)
  - Test debrid provider integrations (`pkg/debrid/providers/`)
  - Test rate limiting and circuit breaker functionality

- [ ] **Task 12: Add concurrency tests**
  - Race condition detection tests
  - Load testing for concurrent operations
  - Goroutine leak detection tests

### Performance Optimization

- [ ] **Task 13: Optimize torrent storage persistence** (`pkg/wire/torrent_storage.go:266-274`)
  - Implement incremental persistence instead of full JSON marshal
  - Add write batching for multiple concurrent updates
  - Consider binary serialization for large datasets

- [ ] **Task 14: Improve queue operations** (`pkg/wire/queue.go:76-114`)
  - Parallelize slot tracking operations
  - Add caching for frequently accessed data
  - Optimize data structures for better performance

- [ ] **Task 15: Optimize cache operations** (`pkg/debrid/store/cache.go:367-400`)
  - Implement streaming for large torrent collections
  - Add pagination support
  - Reduce memory footprint with lazy loading

### Error Handling Standardization

- [ ] **Task 16: Replace silent error handling**
  - Remove `fmt.Println(err)` calls (`pkg/wire/torrent_storage.go:56`)
  - Use structured logging with context
  - Implement proper error propagation

- [ ] **Task 17: Add context propagation**
  - Ensure all service calls receive and use context
  - Implement proper cancellation handling
  - Add timeout configuration for long-running operations

- [ ] **Task 18: Implement error wrapping**
  - Add meaningful error context throughout codebase
  - Create custom error types for different failure modes
  - Improve error reporting and debugging

## ⚠️ Medium Priority (2-3 Sprints)

### Code Quality & Maintainability

- [ ] **Task 19: Refactor large functions and god objects**
  - Break down complex functions in `pkg/wire/store.go`
  - Split `internal/config/config.go` into focused modules
  - Apply single responsibility principle

- [ ] **Task 20: Remove magic numbers and add constants**
  - Define named constants for configuration values
  - Create configuration structure for limits and timeouts
  - Document rationale for chosen values

- [ ] **Task 21: Implement consistent error handling patterns**
  - Standardize on zerolog for all logging
  - Create error handling middleware
  - Add error classification and metrics

### Architectural Improvements

- [ ] **Task 22: Introduce dependency injection**
  - Replace singleton patterns with proper DI
  - Make services testable in isolation
  - Implement interface-based design

- [ ] **Task 23: Reduce tight coupling**
  - Define interfaces for external dependencies
  - Make components swappable
  - Improve separation of concerns

- [ ] **Task 24: Split mixed responsibilities**
  - Separate business logic from infrastructure
  - Extract configuration management
  - Isolate runtime state management

### Go-Specific Best Practices

- [ ] **Task 25: Fix incorrect context usage**
  - Use passed contexts instead of background contexts
  - Implement proper context cancellation
  - Add context timeouts where appropriate

- [ ] **Task 26: Implement proper interface usage**
  - Define interfaces for major components
  - Use interface composition where beneficial
  - Reduce dependency on concrete types

- [ ] **Task 27: Improve channel usage patterns**
  - Replace mutex-protected slices with proper channels (`pkg/wire/request.go:119-246`)
  - Use channel idioms for coordination
  - Implement proper channel closing patterns

- [ ] **Task 28: Optimize string operations**
  - Use string builders for concatenation
  - Cache frequently used strings
  - Optimize string processing in hot paths

## 📊 Low Priority (Long-term)

### Observability & Monitoring

- [ ] **Task 29: Add comprehensive metrics**
  - Implement Prometheus metrics
  - Add custom business metrics
  - Create monitoring dashboards

- [ ] **Task 30: Implement distributed tracing**
  - Add OpenTelemetry support
  - Trace request flows across services
  - Monitor performance bottlenecks

- [ ] **Task 31: Improve logging structure**
  - Add structured logging with consistent fields
  - Implement log levels and filtering
  - Add request correlation IDs

### Performance & Scalability

- [ ] **Task 32: Add connection pooling**
  - Implement HTTP connection pooling
  - Optimize database connections
  - Add connection monitoring

- [ ] **Task 33: Implement caching strategies**
  - Add Redis or in-memory caching
  - Cache frequently accessed data
  - Implement cache invalidation strategies

- [ ] **Task 34: Add performance benchmarks**
  - Create benchmarks for critical paths
  - Set up continuous benchmarking
  - Monitor performance regressions

### Development Experience

- [ ] **Task 35: Add code quality tools**
  - Integrate golangci-lint with CI/CD
  - Add gosec for security scanning
  - Set up automatic code formatting

- [ ] **Task 36: Improve documentation**
  - Add comprehensive API documentation
  - Create development setup guides
  - Document architectural decisions

- [ ] **Task 37: Set up development tooling**
  - Add pre-commit hooks
  - Create debugging configurations
  - Set up profiling tools

## 🔧 Tooling & Infrastructure

### Development Tools
- [ ] Task 38: Set up `golangci-lint` with comprehensive rule set
- [ ] Task 39: Configure `gosec` for security analysis
- [ ] Task 40: Add `pprof` integration for performance profiling
- [ ] Task 41: Set up race detector in CI/CD pipeline

### Testing Tools
- [ ] Task 42: Integrate `testify` for assertions
- [ ] Task 43: Set up `gomock` for mocking
- [ ] Task 44: Add `testcontainers` for integration testing
- [ ] Task 45: Configure test coverage reporting

### Monitoring Tools
- [ ] Task 46: Add `pprof` endpoints for runtime profiling
- [ ] Task 47: Integrate Prometheus metrics
- [ ] Task 48: Set up health check endpoints
- [ ] Task 49: Add structured logging with correlation IDs

## 📈 Success Metrics

- **Race Condition Elimination**: Zero data races detected by race detector
- **Resource Management**: No goroutine or memory leaks under load testing
- **Test Coverage**: >80% test coverage for critical business logic
- **Performance**: <10% performance regression after improvements
- **Security**: All security scan findings resolved
- **Code Quality**: All linting issues resolved

## 📅 Timeline Recommendation

- **Week 1-2**: Fix critical race conditions and resource leaks
- **Week 3-4**: Implement security fixes and basic testing framework
- **Week 5-8**: Performance optimization and comprehensive testing
- **Week 9-12**: Architectural improvements and code quality
- **Week 13+**: Long-term observability and scalability improvements

---

*This task list was generated from automated analysis of the Decypharr codebase. Prioritize tasks based on business impact and risk assessment.*