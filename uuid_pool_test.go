package sfveritas

import (
	"strings"
	"sync"
	"testing"
)

func TestFastUUID_Format(t *testing.T) {
	// Initialize pool
	initUUIDPool()

	id := fastUUID()
	if id == "" {
		t.Fatal("expected non-empty UUID")
	}
	// UUID format: 8-4-4-4-12 hex chars
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Errorf("expected 5 parts separated by dashes, got %d: %s", len(parts), id)
	}
	if len(parts[0]) != 8 {
		t.Errorf("expected first part len 8, got %d", len(parts[0]))
	}
	if len(parts[1]) != 4 {
		t.Errorf("expected second part len 4, got %d", len(parts[1]))
	}
	if len(parts[2]) != 4 {
		t.Errorf("expected third part len 4, got %d", len(parts[2]))
	}
	if len(parts[3]) != 4 {
		t.Errorf("expected fourth part len 4, got %d", len(parts[3]))
	}
	if len(parts[4]) != 12 {
		t.Errorf("expected fifth part len 12, got %d", len(parts[4]))
	}
	if len(id) != 36 {
		t.Errorf("expected total length 36, got %d", len(id))
	}
}

func TestFastUUID_Uniqueness(t *testing.T) {
	initUUIDPool()

	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := fastUUID()
		if seen[id] {
			t.Fatalf("duplicate UUID detected: %s", id)
		}
		seen[id] = true
	}
}

func TestFastUUID_ConcurrentAccess(t *testing.T) {
	initUUIDPool()

	var wg sync.WaitGroup
	ids := make(chan string, 500)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				ids <- fastUUID()
			}
		}()
	}

	wg.Wait()
	close(ids)

	seen := make(map[string]bool)
	for id := range ids {
		if id == "" {
			t.Error("got empty UUID from concurrent access")
		}
		if seen[id] {
			t.Errorf("duplicate UUID from concurrent access: %s", id)
		}
		seen[id] = true
	}
}

func TestFastUUID_FallbackWithoutPool(t *testing.T) {
	// Save pool state and set to nil
	savedPool := pool
	pool = nil
	defer func() { pool = savedPool }()

	id := fastUUID()
	if id == "" {
		t.Fatal("expected non-empty UUID even without pool")
	}
	if len(id) != 36 {
		t.Errorf("expected 36-char UUID, got %d", len(id))
	}
}

func BenchmarkFastUUID(b *testing.B) {
	initUUIDPool()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fastUUID()
	}
}
