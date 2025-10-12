package store

import (
	"strconv"
	"strings"
	"testing"
)

// BenchmarkStringBuilding compares string building approaches
func BenchmarkStringBuilding(b *testing.B) {
	dirs := []string{"dir1", "dir2", "dir3", "dir4", "dir5"}

	b.Run("WithFmtSprint", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var data strings.Builder
			for index, dir := range dirs {
				if dir != "" {
					if index == 0 {
						data.WriteString("dir=" + dir)
					} else {
						// Old approach with fmt.Sprint
						data.WriteString("&dir" + strconv.Itoa(index+1) + "=" + dir)
					}
				}
			}
			_ = data.String()
		}
	})

	b.Run("WithStrconvItoa", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var data strings.Builder
			for index, dir := range dirs {
				if dir != "" {
					if index == 0 {
						data.WriteString("dir=")
					} else {
						data.WriteString("&dir")
						data.WriteString(strconv.Itoa(index + 1))
						data.WriteString("=")
					}
					data.WriteString(dir)
				}
			}
			_ = data.String()
		}
	})

	b.Run("WithCapacityPreallocation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var data strings.Builder
			data.Grow(100) // Pre-allocate estimated size
			for index, dir := range dirs {
				if dir != "" {
					if index == 0 {
						data.WriteString("dir=")
					} else {
						data.WriteString("&dir")
						data.WriteString(strconv.Itoa(index + 1))
						data.WriteString("=")
					}
					data.WriteString(dir)
				}
			}
			_ = data.String()
		}
	})
}

// BenchmarkSlicePreallocation tests slice allocation patterns
func BenchmarkSlicePreallocation(b *testing.B) {
	size := 100

	b.Run("NoPreallocation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			slice := make([]string, 0)
			for j := 0; j < size; j++ {
				slice = append(slice, "item")
			}
		}
	})

	b.Run("WithPreallocation", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			slice := make([]string, 0, size)
			for j := 0; j < size; j++ {
				slice = append(slice, "item")
			}
		}
	})
}
