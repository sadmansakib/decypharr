package wire

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"
)

func keyPair(hash, category string) string {
	return fmt.Sprintf("%s|%s", hash, category)
}

type Torrents = map[string]*Torrent

type TorrentStorage struct {
	torrents     Torrents
	mu           sync.RWMutex
	filename     string // Added to store the filename for persistence
	saveCh       chan saveRequest
	writerCtx    context.Context
	writerCancel context.CancelFunc
	writerDone   chan struct{}
}

// saveRequest represents a request to save the current state to file
type saveRequest struct {
	response chan error
}

func loadTorrentsFromJSON(filename string) (Torrents, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	torrents := make(Torrents)
	if err := json.Unmarshal(data, &torrents); err != nil {
		return nil, err
	}
	return torrents, nil
}

func newTorrentStorage(filename string) *TorrentStorage {
	// Open the JSON file and read the data
	torrents, err := loadTorrentsFromJSON(filename)
	if err != nil {
		torrents = make(Torrents)
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Create a new Storage
	ts := &TorrentStorage{
		torrents:     torrents,
		filename:     filename,
		saveCh:       make(chan saveRequest, 100), // Buffered channel to prevent blocking
		writerCtx:    ctx,
		writerCancel: cancel,
		writerDone:   make(chan struct{}),
	}

	// Start the dedicated writer goroutine
	go ts.writerLoop()

	return ts
}

// NewTorrentStorageForTesting creates a TorrentStorage instance for testing purposes
// This is exported to allow testing of the race condition fix
func NewTorrentStorageForTesting(filename string) *TorrentStorage {
	return newTorrentStorage(filename)
}

// writerLoop is the dedicated goroutine that handles all file write operations
func (ts *TorrentStorage) writerLoop() {
	defer close(ts.writerDone)

	for {
		select {
		case <-ts.writerCtx.Done():
			// Process any remaining save requests before shutting down
			for {
				select {
				case req := <-ts.saveCh:
					err := ts.saveToFileSync()
					req.response <- err
					close(req.response)
				default:
					return
				}
			}
		case req := <-ts.saveCh:
			// Handle save request
			err := ts.saveToFileSync()
			req.response <- err
			close(req.response)
		}
	}
}

// requestSave sends a save request to the writer goroutine
func (ts *TorrentStorage) requestSave() error {
	req := saveRequest{
		response: make(chan error, 1),
	}

	select {
	case ts.saveCh <- req:
		// Wait for the save operation to complete
		return <-req.response
	case <-ts.writerCtx.Done():
		return fmt.Errorf("torrent storage is shutting down")
	}
}

// requestSaveAsync sends a save request to the writer goroutine without waiting for completion
func (ts *TorrentStorage) requestSaveAsync() {
	req := saveRequest{
		response: make(chan error, 1),
	}

	select {
	case ts.saveCh <- req:
		// Don't wait for completion, but handle the response to prevent goroutine leak
		go func() {
			err := <-req.response
			if err != nil {
				fmt.Printf("Async save error: %v\n", err)
			}
		}()
	case <-ts.writerCtx.Done():
		// Storage is shutting down, ignore save request
	default:
		// Channel is full, drop the save request to prevent blocking
		// This is acceptable since we only need the latest state persisted
	}
}

// Close gracefully shuts down the TorrentStorage
func (ts *TorrentStorage) Close() error {
	// Cancel the writer context
	ts.writerCancel()

	// Wait for the writer goroutine to finish
	<-ts.writerDone

	return nil
}

func (ts *TorrentStorage) Add(torrent *Torrent) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents[keyPair(torrent.Hash, torrent.Category)] = torrent

	// Request asynchronous save
	ts.requestSaveAsync()
}

func (ts *TorrentStorage) AddOrUpdate(torrent *Torrent) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents[keyPair(torrent.Hash, torrent.Category)] = torrent

	// Request asynchronous save
	ts.requestSaveAsync()
}

func (ts *TorrentStorage) Get(hash, category string) *Torrent {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	torrent, exists := ts.torrents[keyPair(hash, category)]
	if !exists && category == "" {
		// Try to find the torrent without knowing the category
		for _, t := range ts.torrents {
			if t.Hash == hash {
				return t
			}
		}
	}
	return torrent
}

func (ts *TorrentStorage) GetAll(category string, filter string, hashes []string) []*Torrent {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	torrents := make([]*Torrent, 0)
	for _, torrent := range ts.torrents {
		if category != "" && torrent.Category != category {
			continue
		}
		if filter != "" && torrent.State != filter {
			continue
		}
		torrents = append(torrents, torrent)
	}

	if len(hashes) > 0 {
		filtered := make([]*Torrent, 0)
		for _, hash := range hashes {
			for _, torrent := range torrents {
				if torrent.Hash == hash {
					filtered = append(filtered, torrent)
				}
			}
		}
		torrents = filtered
	}
	return torrents
}

func (ts *TorrentStorage) GetAllSorted(category string, filter string, hashes []string, sortBy string, ascending bool) []*Torrent {
	torrents := ts.GetAll(category, filter, hashes)
	if sortBy != "" {
		sort.Slice(torrents, func(i, j int) bool {
			// If ascending is false, swap i and j to get descending order
			if !ascending {
				i, j = j, i
			}

			switch sortBy {
			case "name":
				return torrents[i].Name < torrents[j].Name
			case "size":
				return torrents[i].Size < torrents[j].Size
			case "added_on":
				return torrents[i].AddedOn < torrents[j].AddedOn
			case "completed":
				return torrents[i].Completed < torrents[j].Completed
			case "progress":
				return torrents[i].Progress < torrents[j].Progress
			case "state":
				return torrents[i].State < torrents[j].State
			case "category":
				return torrents[i].Category < torrents[j].Category
			case "dlspeed":
				return torrents[i].Dlspeed < torrents[j].Dlspeed
			case "upspeed":
				return torrents[i].Upspeed < torrents[j].Upspeed
			case "ratio":
				return torrents[i].Ratio < torrents[j].Ratio
			default:
				// Default sort by added_on
				return torrents[i].AddedOn < torrents[j].AddedOn
			}
		})
	}
	return torrents
}

func (ts *TorrentStorage) Update(torrent *Torrent) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents[keyPair(torrent.Hash, torrent.Category)] = torrent

	// Request asynchronous save
	ts.requestSaveAsync()
}

func (ts *TorrentStorage) Delete(hash, category string, removeFromDebrid bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	wireStore := Get()
	for key, torrent := range ts.torrents {
		if torrent == nil {
			continue
		}
		if torrent.Hash == hash && (category == "" || torrent.Category == category) {
			if torrent.State == "queued" && torrent.ID != "" {
				// Remove the torrent from the import queue if it exists
				wireStore.importsQueue.Delete(torrent.ID)
			}
			if removeFromDebrid && torrent.DebridID != "" && torrent.Debrid != "" {
				dbClient := wireStore.debrid.Client(torrent.Debrid)
				if dbClient != nil {
					_ = dbClient.DeleteTorrent(torrent.DebridID)
				}
			}
			delete(ts.torrents, key)

			// Delete the torrent folder - log error but continue cleanup
			if torrent.ContentPath != "" {
				err := os.RemoveAll(torrent.ContentPath)
				if err != nil {
					wireStore.logger.Error().Err(err).Str("path", torrent.ContentPath).Msg("Failed to remove torrent content directory")
				}
			}
			break
		}
	}

	// Request asynchronous save
	ts.requestSaveAsync()
}

func (ts *TorrentStorage) DeleteMultiple(hashes []string, removeFromDebrid bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	toDelete := make(map[string]string)

	st := Get()

	for _, hash := range hashes {
		for key, torrent := range ts.torrents {
			if torrent == nil {
				continue
			}
			if torrent.Hash == hash {
				if torrent.State == "queued" && torrent.ID != "" {
					// Remove the torrent from the import queue if it exists
					st.importsQueue.Delete(torrent.ID)
				}
				if removeFromDebrid && torrent.DebridID != "" && torrent.Debrid != "" {
					toDelete[torrent.DebridID] = torrent.Debrid
				}
				delete(ts.torrents, key)
				if torrent.ContentPath != "" {
					err := os.RemoveAll(torrent.ContentPath)
					if err != nil {
						st.logger.Error().Err(err).Str("path", torrent.ContentPath).Msg("Failed to remove torrent content directory")
					}
				}
				break
			}
		}
	}

	// Request asynchronous save
	ts.requestSaveAsync()

	clients := st.debrid.Clients()

	go func() {
		for id, debrid := range toDelete {
			dbClient, ok := clients[debrid]
			if !ok {
				continue
			}
			err := dbClient.DeleteTorrent(id)
			if err != nil {
				fmt.Println(err)
			}
		}
	}()
}

func (ts *TorrentStorage) Save() error {
	return ts.requestSave()
}

// saveToFileSync is the synchronous version that performs the actual file write
// This method should only be called from the writer goroutine
func (ts *TorrentStorage) saveToFileSync() error {
	ts.mu.RLock()
	data, err := json.MarshalIndent(ts.torrents, "", "  ")
	ts.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(ts.filename, data, 0644)
}

// saveToFile is deprecated but kept for backward compatibility
// Use requestSave() or requestSaveAsync() instead
func (ts *TorrentStorage) saveToFile() error {
	return ts.requestSave()
}

func (ts *TorrentStorage) Reset() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.torrents = make(Torrents)
}

// GetStalledTorrents returns a list of torrents that are stalled
// A torrent is considered stalled if it has no seeds, no progress, and has been downloading for longer than removeStalledAfter
// The torrent must have a DebridID and be in the "downloading" state
func (ts *TorrentStorage) GetStalledTorrents(removeAfter time.Duration) []*Torrent {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	stalled := make([]*Torrent, 0)
	currentTime := time.Now()
	for _, torrent := range ts.torrents {
		if torrent.DebridID != "" && torrent.State == "downloading" && torrent.NumSeeds == 0 && torrent.Progress == 0 {
			addedOn := time.Unix(torrent.AddedOn, 0)
			if currentTime.Sub(addedOn) > removeAfter {
				stalled = append(stalled, torrent)
			}
		}
	}
	return stalled
}
