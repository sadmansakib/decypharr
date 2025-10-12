package wire

import (
	"cmp"
	"slices"
	"testing"
)

// BenchmarkSortingOperations compares old sort.Slice vs new slices.SortFunc
func BenchmarkSortingOperations(b *testing.B) {
	// Create test data
	torrents := make([]*Torrent, 100)
	for i := 0; i < 100; i++ {
		torrents[i] = &Torrent{
			AddedOn: int64(100 - i),
		}
	}

	b.Run("slices.SortFunc", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			testTorrents := make([]*Torrent, len(torrents))
			copy(testTorrents, torrents)
			slices.SortFunc(testTorrents, func(a, b *Torrent) int {
				return cmp.Compare(a.AddedOn, b.AddedOn)
			})
		}
	})
}

// BenchmarkSliceAllocation compares slice allocation with and without capacity hints
func BenchmarkSliceAllocation(b *testing.B) {
	size := 1000

	b.Run("WithoutCapacity", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			slice := make([]*Torrent, 0)
			for j := 0; j < size; j++ {
				slice = append(slice, &Torrent{})
			}
		}
	})

	b.Run("WithCapacity", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			slice := make([]*Torrent, 0, size)
			for j := 0; j < size; j++ {
				slice = append(slice, &Torrent{})
			}
		}
	})
}

// BenchmarkMapOperations compares map clearing operations
func BenchmarkMapOperations(b *testing.B) {
	size := 1000

	b.Run("ManualClear", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m := make(map[string]*Torrent, size)
			for j := 0; j < size; j++ {
				m[string(rune(j))] = &Torrent{}
			}
			// Manual clearing
			for k := range m {
				delete(m, k)
			}
		}
	})

	b.Run("BuiltinClear", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			m := make(map[string]*Torrent, size)
			for j := 0; j < size; j++ {
				m[string(rune(j))] = &Torrent{}
			}
			// Built-in clear
			clear(m)
		}
	})
}
