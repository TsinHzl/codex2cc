package security

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
)

const DefaultRequestMemoryBudgetBytes int64 = 128 << 20

const requestMemoryContextKey = "request_memory_reservation"

var ErrRequestMemoryBudget = errors.New("request memory budget exhausted")

// RequestMemorySnapshot counts retained logical request bytes, not allocator
// capacity, JSON working copies, or process RSS. Admission is fail-fast so
// requests do not wait for memory while already retaining large bodies.
type RequestMemorySnapshot struct {
	LimitBytes         int64  `json:"limit_bytes"`
	UsedBytes          int64  `json:"used_bytes"`
	HighWaterBytes     int64  `json:"high_water_bytes"`
	ActiveReservations int64  `json:"active_reservations"`
	Rejections         uint64 `json:"rejections"`
}

type requestMemoryBudget struct {
	mu sync.Mutex
	RequestMemorySnapshot
}

var processRequestMemory = &requestMemoryBudget{RequestMemorySnapshot: RequestMemorySnapshot{LimitBytes: DefaultRequestMemoryBudgetBytes}}

// ConfigureRequestMemoryBudget is called during startup. Existing reservations
// remain charged if a test or future caller changes the limit while in use.
func ConfigureRequestMemoryBudget(limit int64) {
	if limit <= 0 {
		limit = DefaultRequestMemoryBudgetBytes
	}
	processRequestMemory.mu.Lock()
	processRequestMemory.LimitBytes = limit
	processRequestMemory.mu.Unlock()
}

func GetRequestMemorySnapshot() RequestMemorySnapshot {
	processRequestMemory.mu.Lock()
	defer processRequestMemory.mu.Unlock()
	return processRequestMemory.RequestMemorySnapshot
}

// RequestMemoryReservation may be shared by queue message copies. Release is
// idempotent; a released reservation cannot be grown again.
type RequestMemoryReservation struct {
	mu       sync.Mutex
	budget   *requestMemoryBudget
	bytes    int64
	released bool
}

func TryAcquireRequestMemory(size int64) (*RequestMemoryReservation, bool) {
	return processRequestMemory.acquire(size)
}

func (b *requestMemoryBudget) acquire(size int64) (*RequestMemoryReservation, bool) {
	r := &RequestMemoryReservation{budget: b}
	if !r.grow(size) {
		return nil, false
	}
	return r, true
}

func (r *RequestMemoryReservation) grow(size int64) bool {
	if r == nil || size < 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return false
	}
	if size > (1<<63-1)-r.bytes {
		return false
	}
	return r.resizeLocked(r.bytes + size)
}

// TryResize adjusts a retained session snapshot without briefly releasing its
// existing admission. Shrinking and Release always return capacity promptly.
func (r *RequestMemoryReservation) TryResize(size int64) bool {
	if r == nil || size < 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return false
	}
	return r.resizeLocked(size)
}

func (r *RequestMemoryReservation) resizeLocked(size int64) bool {
	b := r.budget
	b.mu.Lock()
	defer b.mu.Unlock()
	delta := size - r.bytes
	if delta > 0 && delta > b.LimitBytes-b.UsedBytes {
		b.Rejections++
		return false
	}
	if r.bytes == 0 && size > 0 {
		b.ActiveReservations++
	} else if r.bytes > 0 && size == 0 {
		b.ActiveReservations--
	}
	r.bytes = size
	b.UsedBytes += delta
	if b.UsedBytes > b.HighWaterBytes {
		b.HighWaterBytes = b.UsedBytes
	}
	return true
}

func (r *RequestMemoryReservation) Release() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return
	}
	r.released = true
	r.budget.mu.Lock()
	if r.bytes > 0 {
		r.budget.UsedBytes -= r.bytes
		r.budget.ActiveReservations--
	}
	r.bytes = 0
	r.budget.mu.Unlock()
}

type requestMemoryReader struct {
	reader      io.Reader
	reservation *RequestMemoryReservation
	prepaid     int64
}

func (r *requestMemoryReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	charge := int64(n)
	if charge <= r.prepaid {
		r.prepaid -= charge
		return n, err
	}
	charge -= r.prepaid
	r.prepaid = 0
	if !r.reservation.grow(charge) {
		return 0, ErrRequestMemoryBudget
	}
	return n, err
}

func readRequestMemoryBounded(reader io.Reader, maxSize int64, reservation *RequestMemoryReservation, prepaid int64) ([]byte, error) {
	if reservation != nil {
		reader = &requestMemoryReader{reader: reader, reservation: reservation, prepaid: prepaid}
	}
	return io.ReadAll(io.LimitReader(reader, maxSize+1))
}

// ReadRequestMemory charges fragmented/unknown-length WS input as it arrives.
// Ownership is transferred to the caller only on success; cancellation/read
// failures return all previously charged bytes.
func ReadRequestMemory(reader io.Reader, maxSize int64) ([]byte, *RequestMemoryReservation, error) {
	r, _ := TryAcquireRequestMemory(0)
	data, err := readRequestMemoryBounded(reader, maxSize, r, 0)
	if err != nil {
		r.Release()
		return nil, nil, err
	}
	if int64(len(data)) > maxSize {
		r.Release()
		return nil, nil, errors.New("websocket request body exceeds size limit")
	}
	return data, r, nil
}

func requestMemoryFromContext(c *gin.Context) *RequestMemoryReservation {
	value, _ := c.Get(requestMemoryContextKey)
	r, _ := value.(*RequestMemoryReservation)
	return r
}

func rejectRequestMemory(c *gin.Context) {
	c.Header("Retry-After", "1")
	c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
		"message": "Server request memory capacity is temporarily exhausted; retry later",
		"type":    "server_error", "code": "service_unavailable",
	}})
}
