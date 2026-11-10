package learn

import "testing"

func TestAMemoryLimitIsLearnedFromTheTailOfHealthyUse(t *testing.T) {
	small := make([]float64, 100)
	for i := range small {
		small[i] = float64(6+i%2) * mebibyte // 6 and 7 MiB, as agnhost uses
	}
	// The 99th percentile is 7 MiB; 8 MiB of room beats 30% of it.
	if got, err := MemoryLimit(small); err != nil || got != "15Mi" {
		t.Fatalf("small: %s, %v; want 15Mi", got, err)
	}
	large := make([]float64, 100)
	for i := range large {
		large[i] = float64(900+i) * mebibyte
	}
	// The 99th percentile is 998 MiB; 30% of it is the room: 1297.4 MiB.
	if got, err := MemoryLimit(large); err != nil || got != "1298Mi" {
		t.Fatalf("large: %s, %v; want 1298Mi", got, err)
	}
	if _, err := MemoryLimit(small[:MinSamples-1]); err == nil {
		t.Fatal("a limit was learned from too few samples")
	}
	if q := Quantile([]float64{3, 1, 2, 4}, 0.5); q != 2 {
		t.Fatalf("median of 1..4 = %v, want 2", q)
	}
}

func TestADiskLimitMatchesMemoryLearning(t *testing.T) {
	small := make([]float64, 100)
	for i := range small {
		small[i] = float64(6+i%2) * mebibyte
	}
	if got, err := DiskLimit(small); err != nil || got != "15Mi" {
		t.Fatalf("disk: %s, %v; want 15Mi", got, err)
	}
}

func TestACPULimitIsLearnedFromTheTailOfHealthyUse(t *testing.T) {
	small := make([]float64, 100)
	for i := range small {
		small[i] = float64(50+10*(i%2)) / 1000 // 50m and 60m
	}
	// The 99th percentile is 60m; 20m of room beats 30% of it.
	if got, err := CPULimit(small); err != nil || got != "80m" {
		t.Fatalf("small: %s, %v; want 80m", got, err)
	}
	large := make([]float64, 100)
	for i := range large {
		large[i] = float64(900+i) / 1000
	}
	// The 99th percentile is 998m; 30% of it is the room: 1297.4m.
	if got, err := CPULimit(large); err != nil || got != "1298m" {
		t.Fatalf("large: %s, %v; want 1298m", got, err)
	}
	if _, err := CPULimit(small[:MinSamples-1]); err == nil {
		t.Fatal("a limit was learned from too few samples")
	}
}
