package gateway

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	defaultMaxConnsPerKey = 2
	defaultConnTTL        = 5 * time.Minute
)

// pooledConn wraps an AMQP connection with its creation time.
type pooledConn struct {
	conn      *amqp.Connection
	createdAt time.Time
}

// connPool manages a pool of AMQP connections keyed by user:password:vhost.
type connPool struct {
	mu       sync.Mutex
	maxConns int
	ttl      time.Duration
	conns    map[string][]pooledConn
}

func newConnPool(maxConns int, ttl time.Duration) *connPool {
	if maxConns <= 0 {
		maxConns = defaultMaxConnsPerKey
	}
	if ttl <= 0 {
		ttl = defaultConnTTL
	}
	return &connPool{
		maxConns: maxConns,
		ttl:      ttl,
		conns:    make(map[string][]pooledConn),
	}
}

func poolKey(username, password, vhost string) string {
	return fmt.Sprintf("%s:%s@%s", username, password, vhost)
}

// get retrieves an idle connection from the pool, or dials a new one.
func (p *connPool) get(dialURL, username, password, vhost string) (*amqp.Connection, error) {
	key := poolKey(username, password, vhost)
	now := time.Now()

	p.mu.Lock()
	for len(p.conns[key]) > 0 {
		n := len(p.conns[key])
		pc := p.conns[key][n-1]
		p.conns[key] = p.conns[key][:n-1]
		p.mu.Unlock()

		if pc.conn.IsClosed() {
			slog.Debug("discarding closed pooled connection", "key", key)
			p.mu.Lock()
			continue
		}
		if now.Sub(pc.createdAt) > p.ttl {
			slog.Debug("discarding expired pooled connection", "key", key, "age", now.Sub(pc.createdAt).String())
			pc.conn.Close()
			p.mu.Lock()
			continue
		}

		slog.Debug("reusing pooled connection", "key", key)
		return pc.conn, nil
	}
	p.mu.Unlock()

	slog.Debug("dialing new connection", "key", key)
	return amqp.Dial(dialURL)
}

// put returns a connection to the pool. If the pool is full,
// the connection is expired, or closed, it is discarded.
func (p *connPool) put(conn *amqp.Connection, username, password, vhost string, createdAt time.Time) {
	if conn.IsClosed() {
		return
	}
	if time.Since(createdAt) > p.ttl {
		conn.Close()
		return
	}

	key := poolKey(username, password, vhost)
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.conns[key]) >= p.maxConns {
		conn.Close()
		return
	}
	p.conns[key] = append(p.conns[key], pooledConn{conn: conn, createdAt: createdAt})
}

// discard closes a connection without returning it to the pool.
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
		for _, pc := range conns {
			pc.conn.Close()
		}
		delete(p.conns, key)
	}
}
