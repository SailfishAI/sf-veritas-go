package sfveritas

import (
	"sync"

	"github.com/google/uuid"
)

// uuidPool pre-generates UUIDs in a ring buffer to avoid calling
// crypto/rand.Read() (a syscall) on every span/request/trace.
// The Python reference implementation uses this same pattern for ~26x speedup.
//
// The pool is filled by a background goroutine. When empty, it falls back to
// synchronous uuid.New() so correctness is never compromised.

const uuidPoolSize = 4096

var (
	pool     chan string
	poolOnce sync.Once
)

func initUUIDPool() {
	poolOnce.Do(func() {
		pool = make(chan string, uuidPoolSize)
		go fillUUIDPool(pool)
	})
}

func fillUUIDPool(ch chan string) {
	for {
		id := uuid.New().String()
		ch <- id // blocks when pool is full, which is fine — back-pressure
	}
}

// fastUUID returns a pre-generated UUID string from the pool.
// Falls back to synchronous generation if the pool is empty.
func fastUUID() string {
	if pool == nil {
		return uuid.New().String()
	}
	select {
	case id := <-pool:
		return id
	default:
		// Pool exhausted; fall back to synchronous generation
		return uuid.New().String()
	}
}
