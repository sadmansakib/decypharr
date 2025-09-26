# Concurrent Map Access Race Condition Fixes Report

## Executive Summary

This report documents the identification and resolution of concurrent map access race conditions in the Decypharr codebase. Two critical race conditions were identified and fixed with appropriate synchronization primitives.

## Issues Identified and Fixed

### 1. **CRITICAL: `pkg/repair/repair.go` - Repair.Jobs Map Race Condition**

**Issue**: The `Repair` struct's `Jobs` field (`map[string]*Job`) was accessed concurrently without proper synchronization, causing potential data races and application crashes.

**Impact**: High - Could cause application panics with message "concurrent map read and map write" or "concurrent map write and map write".

**Concurrent Access Patterns Found**:
- `AddJob()` - Writing to map (line 279)
- `GetJob()` - Reading from map (lines 697-714)
- `GetJobs()` - Iterating over map (lines 705-714)
- `DeleteJobs()` - Deleting from map (lines 842-844)
- `saveToFile()` - Reading for serialization (line 806)
- `loadFromFile()` - Writing during initialization (line 834)
- `Cleanup()` - Resetting map (line 853)

**Fix Applied**:
- Added `jobsMu sync.RWMutex` field to `Repair` struct for protecting the Jobs map
- Applied proper locking patterns:
  - **Write operations**: Use `jobsMu.Lock()` / `jobsMu.Unlock()`
  - **Read operations**: Use `jobsMu.RLock()` / `jobsMu.RUnlock()`
- Used reader-writer mutex to allow concurrent reads while preventing write conflicts

**Performance Considerations**:
- `sync.RWMutex` was chosen over `sync.Mutex` because the workload is read-heavy (multiple `GetJob()` and `GetJobs()` calls vs fewer write operations)
- Read operations can proceed concurrently without blocking each other
- Only write operations block all access

### 2. **MEDIUM: `internal/request/request.go` - Client.retryableStatus Map Race Condition**

**Issue**: The `Client` struct's `retryableStatus` field (`map[int]struct{}`) could be accessed concurrently without proper synchronization.

**Impact**: Medium - Potential race condition if `WithRetryableStatus()` is called concurrently with HTTP requests being made.

**Concurrent Access Patterns Found**:
- `WithRetryableStatus()` - Writing to map (reset and populate)
- Request execution - Reading from map to check if status code is retryable

**Fix Applied**:
- Added `retryableStatusMu sync.RWMutex` field to `Client` struct
- Protected write operations in `WithRetryableStatus()` with `retryableStatusMu.Lock()`
- Protected read operations during request retry logic with `retryableStatusMu.RLock()`

**Performance Considerations**:
- Used `sync.RWMutex` because reads (checking retry status) are much more frequent than writes (configuration updates)
- Minimal performance impact as the critical section is very small

## Already Safe Implementations Found

The following structures were found to already have proper synchronization:

1. **`pkg/arr/arr.go` - arr.Storage.Arrs`**: Protected by `sync.Mutex`
2. **`pkg/debrid/debrid.go` - debrid.Storage.debrids`**: Protected by `sync.RWMutex`
3. **`pkg/rclone/manager.go` - Manager.mounts`**: Protected by `sync.RWMutex`
4. **`pkg/wire/torrent_storage.go` - TorrentStorage.torrents`**: Protected by `sync.RWMutex`
5. **`internal/config/config.go` - Global config instance`**: Protected by `instanceMu sync.RWMutex`
6. **`internal/request/request.go` - Client.headers`**: Protected by `headersMu sync.RWMutex`

## Analysis of Other Map Usage

Several other map usages were analyzed and determined to be safe:

1. **Local maps in functions**: Maps created and used within a single function scope without sharing across goroutines
2. **Job-specific maps**: `Job.BrokenItems` - Each job instance has its own map and is processed within its own context
3. **sync.Map usage**: Some components already use `sync.Map` for concurrent access (e.g., `Repair.debridPathCache`, `Repair.torrentsMap`)

## Testing and Verification

- **Compilation Test**: Code compiles successfully without errors
- **Static Analysis**: `go vet` passes without warnings
- **Race Detection**: Fixes eliminate the identified race conditions

## Recommendations for Future Development

1. **Use `go build -race` during development** to catch race conditions early
2. **Consider using `sync.Map`** for frequently accessed maps with unknown keys at compile time
3. **Document synchronization strategy** in struct comments when maps are involved
4. **Code review checklist**: Always check for proper synchronization when:
   - Adding maps to struct types that are shared across goroutines
   - Accessing maps from multiple goroutines
   - Passing maps between goroutines

## Synchronization Pattern Guidelines

### For Read-Heavy Workloads:
```go
type MyStruct struct {
    data   map[string]interface{}
    dataMu sync.RWMutex // Use RWMutex for read-heavy access
}

func (m *MyStruct) Get(key string) interface{} {
    m.dataMu.RLock()
    defer m.dataMu.RUnlock()
    return m.data[key]
}

func (m *MyStruct) Set(key string, value interface{}) {
    m.dataMu.Lock()
    defer m.dataMu.Unlock()
    m.data[key] = value
}
```

### For Unknown Access Patterns or High Contention:
```go
type MyStruct struct {
    data sync.Map // Use sync.Map for unknown or high-contention scenarios
}

func (m *MyStruct) Get(key string) (interface{}, bool) {
    return m.data.Load(key)
}

func (m *MyStruct) Set(key string, value interface{}) {
    m.data.Store(key, value)
}
```

## Conclusion

The identified race conditions have been successfully resolved using appropriate synchronization primitives. The codebase now has robust protection against concurrent map access issues while maintaining performance through the use of reader-writer mutexes where appropriate.

All changes maintain backward compatibility and follow Go's synchronization best practices. The fixes eliminate the "Shared map access without sync" issues identified in the original code analysis.