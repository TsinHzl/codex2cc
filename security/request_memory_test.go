package security

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestMemoryRejectsConcurrentBodiesAndRecovers(t *testing.T) {
	budget := &requestMemoryBudget{RequestMemorySnapshot: RequestMemorySnapshot{LimitBytes: 64}}
	router := gin.New()
	router.Use(requestSizeLimiterWithMemory(128, budget))
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	router.POST("/hold", func(c *gin.Context) { close(entered); <-release; c.Status(200) })
	router.POST("/quick", func(c *gin.Context) { c.Status(200) })
	go func() {
		defer close(finished)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("POST", "/hold", strings.NewReader(strings.Repeat("x", 64))))
	}()
	<-entered
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/quick", strings.NewReader("x")))
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" || !strings.Contains(w.Body.String(), "service_unavailable") {
		t.Errorf("overload response = %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	close(release)
	<-finished
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/quick", strings.NewReader("x")))
	if w.Code != 200 || budget.UsedBytes != 0 || budget.ActiveReservations != 0 {
		t.Fatalf("did not recover: status=%d snapshot=%+v", w.Code, budget.RequestMemorySnapshot)
	}
}

func TestRequestMemoryChargesUnknownLengthAndDecompressedBytes(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "chunked", true: "gzip"}[compressed], func(t *testing.T) {
			budget := &requestMemoryBudget{RequestMemorySnapshot: RequestMemorySnapshot{LimitBytes: 128}}
			router := gin.New()
			router.Use(requestSizeLimiterWithMemory(1024, budget), RequestBodyDecompressor(1024))
			router.POST("/", func(c *gin.Context) { t.Error("over-budget handler ran"); c.Status(200) })
			body := []byte(strings.Repeat("x", 512))
			if compressed {
				var encoded bytes.Buffer
				writer := gzip.NewWriter(&encoded)
				if _, err := writer.Write(body); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				body = encoded.Bytes()
			}
			req := httptest.NewRequest("POST", "/", bytes.NewReader(body))
			if compressed {
				req.Header.Set("Content-Encoding", "gzip")
			} else {
				req.ContentLength = -1
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != 503 || budget.UsedBytes != 0 || budget.ActiveReservations != 0 {
				t.Fatalf("status=%d snapshot=%+v body=%s", w.Code, budget.RequestMemorySnapshot, w.Body.String())
			}
		})
	}
}

func TestRequestMemorySingleRequestSizeStillReturns413(t *testing.T) {
	budget := &requestMemoryBudget{RequestMemorySnapshot: RequestMemorySnapshot{LimitBytes: 8}}
	router := gin.New()
	router.Use(requestSizeLimiterWithMemory(16, budget))
	router.POST("/", func(c *gin.Context) { t.Error("oversize handler ran") })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", 17))))
	if w.Code != http.StatusRequestEntityTooLarge || budget.UsedBytes != 0 {
		t.Fatalf("status=%d bytes=%d", w.Code, budget.UsedBytes)
	}
}

func TestRequestMemoryResizeAndConcurrentRelease(t *testing.T) {
	budget := &requestMemoryBudget{RequestMemorySnapshot: RequestMemorySnapshot{LimitBytes: 100}}
	r, ok := budget.acquire(80)
	if !ok || r.TryResize(101) || !r.TryResize(20) {
		t.Fatal("resize admission")
	}
	other, ok := budget.acquire(80)
	if !ok {
		t.Fatal("shrink did not return capacity")
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Release(); other.Release() }()
	}
	wg.Wait()
	if budget.UsedBytes != 0 || budget.ActiveReservations != 0 || r.TryResize(1) {
		t.Fatalf("released reservations = %+v", budget.RequestMemorySnapshot)
	}
}
