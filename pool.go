package gateway

import (
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

const defaultMaxConnsPerKey = 2

// connPool manages a pool of AMQP connections keyed by user:vhost.
type connPool struct {
	mu       sync.Mutex
	maxConns int
	conns    map[string][]*amqp.Connection
}

func newConnPool(maxConns int) *connPool {
	if maxConns <= 0 {
		maxConns = defaultMaxConnsPerKey
	}
	return &connPool{
		maxConns: maxConns,
		conns:    make(map[string][]*amqp.Connection),
	}
}

func poolKey(username, vhost string) string {
	return fmt.Sprintf("%s@%s", username, vhost)
}

// get retrieves an idle connection from the pool, or dials a new one.
// The returned connection may be closed by the server; callers should
// handle errors and discard the connection without returning it.
func (p *connPool) get(dialURL, username, vhost string) (*amqp.Connection, error) {
	key := poolKey(username, vhost)

	p.mu.Lock()
	for len(p.conns[key]) > 0 {
		// Pop from the end
		n := len(p.conns[key])
		conn := p.conns[key][n-1]
		p.conns[key] = p.conns[key][:n-1]
		p.mu.Unlock()

		// Check if the connection is still alive
		if !conn.IsClosed() {
			slog.Debug("reusing pooled connection", "key", key)
			return conn, nil
		}
		// Connection is stale, discard and try next
		slog.Debug("discarding stale pooled connection", "key", key)

		p.mu.Lock()
	}
	p.mu.Unlock()

	slog.Debug("dialing new connection", "key", key)
	return amqp.Dial(dialURL)
}

// put returns a connection to the pool. If the pool is full or
// the connection is closed, the connection is closed and discarded.
func (p *connPool) put(conn *amqp.Connection, username, vhost string) {
	if conn.IsClosed() {
		return
	}

	key := poolKey(username, vhost)
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.conns[key]) >= p.maxConns {
		conn.Close()
		return
	}
	p.conns[key] = append(p.conns[key], conn)
}

// discard closes a connection without returning it to the pool.
// Use this when an error occurred and the connection may be broken.
func (p *connPool) discard(conn *amqp.Connection) {
	if conn != nil {
		conn.Close()
	}
}

// closeAll closes all pooled connections.
func (p *connPool) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key, conns := range p.conns {
		for _, conn := range conns {
			conn.Close()
		}
		delete(p.conns, key)
	}
}
