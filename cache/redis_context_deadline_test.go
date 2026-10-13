package cache

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// The real production constructor talks to a disposable RESP peer. Handshake
// replies are immediate; command replies wait until cleanup, so the test
// measures the client's I/O deadline without touching a deployed Redis.
func newDeadlineRedisCache(t *testing.T) TokenCache {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns sync.Map
	release := make(chan struct{})
	var handlers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conns.Store(conn, struct{}{})
			handlers.Add(1)
			go func() {
				defer handlers.Done()
				defer conn.Close()
				defer conns.Delete(conn)
				reader := bufio.NewReader(conn)
				for {
					args, err := readRESPCommand(reader)
					if err != nil || len(args) == 0 {
						return
					}
					reply := "+OK\r\n"
					switch strings.ToUpper(args[0]) {
					case "HELLO":
						reply = "%0\r\n"
					case "PING":
						reply = "+PONG\r\n"
					case "CLIENT":
					default:
						<-release
						return
					}
					if _, err = io.WriteString(conn, reply); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-acceptDone
		close(release)
		conns.Range(func(k, v any) bool { k.(net.Conn).Close(); return true })
		handlers.Wait()
	})
	tc, err := NewRedis(listener.Addr().String(), "", 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tc.Close() })
	return tc
}

func TestRedisContextDeadlinesCoverWritesAuthenticationLeasesAndCounters(t *testing.T) {
	tests := []struct {
		name string
		run  func(context.Context, TokenCache) error
	}{
		{"response_context", func(ctx context.Context, c TokenCache) error {
			return c.SetResponseContext(ctx, "test-response", []json.RawMessage{json.RawMessage(`{"type":"message","content":"hello"}`)}, time.Minute)
		}},
		{"access_token_read", func(ctx context.Context, c TokenCache) error { _, err := c.GetAccessToken(ctx, 1); return err }},
		{"access_token_write", func(ctx context.Context, c TokenCache) error {
			return c.SetAccessToken(ctx, 1, "synthetic-test-token", time.Minute)
		}},
		{"refresh_lock", func(ctx context.Context, c TokenCache) error {
			_, err := c.AcquireRefreshLock(ctx, 1, time.Minute)
			return err
		}},
		{"lease_acquire", func(ctx context.Context, c TokenCache) error {
			_, err := c.AcquireLease(ctx, "test", "key", "owner", time.Minute)
			return err
		}},
		{"lease_release", func(ctx context.Context, c TokenCache) error { return c.ReleaseLease(ctx, "test", "key", "owner") }},
		{"counter_pipeline", func(ctx context.Context, c TokenCache) error {
			return c.IncrRuntimeCounters(ctx, "test", "key", map[string]float64{"count": 1}, time.Minute)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := newDeadlineRedisCache(t)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := tt.run(ctx, tc)
			elapsed := time.Since(start)
			t.Logf("deadline=50ms elapsed=%s err=%v", elapsed, err)
			if err == nil || ctx.Err() == nil || elapsed > 500*time.Millisecond {
				t.Fatalf("Redis I/O ignored context deadline: elapsed=%s err=%v ctx=%v", elapsed, err, ctx.Err())
			}
		})
	}
}
