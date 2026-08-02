package adminapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/HW-Yue/Memora/internal/adminui"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

const (
	SessionVersion  = "memora.admin-session/v1"
	ErrorVersion    = "memora.admin-error/v1"
	DefaultAddress  = "127.0.0.1:3888"
	MaxRequestBytes = 256 << 10
	maxStatements   = 32
	defaultTTL      = 15 * time.Minute
	maxTTL          = time.Hour
	sessionCookie   = "memora_admin_session"
)

type ExecuteFunc func(
	context.Context,
	string,
	string,
	[]executor.StatementInput,
) (result.Envelope, error)

type Config struct {
	DataDir    string
	Scopes     []string
	Execute    ExecuteFunc
	Random     io.Reader
	Now        func() time.Time
	Listen     func(network, address string) (net.Listener, error)
	SessionTTL time.Duration
}

type Descriptor struct {
	Version   string    `json:"version"`
	Origin    string    `json:"origin"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type SessionReceipt struct {
	Version   string    `json:"version"`
	CSRFToken string    `json:"csrf_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Gateway struct {
	descriptor Descriptor
	dataDir    string
	scopes     []string
	execute    ExecuteFunc
	now        func() time.Time

	mu                sync.Mutex
	bootstrapHash     [sha256.Size]byte
	sessionHash       [sha256.Size]byte
	csrfHash          [sha256.Size]byte
	sessionToken      string
	csrfToken         string
	bootstrapConsumed bool

	server    *http.Server
	listener  net.Listener
	done      chan struct{}
	closeOnce sync.Once
	errMu     sync.Mutex
	serveErr  error
}

func Start(ctx context.Context, config Config) (*Gateway, error) {
	if ctx == nil {
		return nil, errors.New("admin API context is nil")
	}
	if !filepath.IsAbs(config.DataDir) {
		return nil, errors.New("admin API data directory must be absolute")
	}
	if config.Execute == nil {
		return nil, errors.New("admin API execute function is nil")
	}
	shell, err := adminui.Embedded()
	if err != nil {
		return nil, fmt.Errorf("load embedded admin shell: %w", err)
	}
	ttl := config.SessionTTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < time.Second || ttl > maxTTL {
		return nil, errors.New("admin API session TTL is outside 1 second to 1 hour")
	}
	authorization := security.Authorization{
		Version:             security.AuthorizationVersion,
		Actor:               "user:admin",
		AuthorizedDatabases: append([]string(nil), config.Scopes...),
	}
	if err := authorization.Validate(); err != nil {
		return nil, fmt.Errorf("admin API scope: %w", err)
	}

	random := config.Random
	if random == nil {
		random = rand.Reader
	}
	secrets := make([]byte, 96)
	if _, err := io.ReadFull(random, secrets); err != nil {
		return nil, fmt.Errorf("generate admin API session secrets: %w", err)
	}
	bootstrap := encodeToken(secrets[:32])
	session := encodeToken(secrets[32:64])
	csrf := encodeToken(secrets[64:])

	listen := config.Listen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp4", DefaultAddress)
	if err != nil {
		return nil, fmt.Errorf("listen for admin API: %w", err)
	}
	if err := validateListener(listener); err != nil {
		_ = listener.Close()
		return nil, err
	}

	now := config.Now
	if now == nil {
		now = time.Now
	}
	expiresAt := now().UTC().Add(ttl)
	host := listener.Addr().String()
	origin := "http://" + host
	fragment := url.Values{"token": []string{bootstrap}}.Encode()
	gateway := &Gateway{
		descriptor: Descriptor{
			Version:   SessionVersion,
			Origin:    origin,
			URL:       origin + "/#" + fragment,
			ExpiresAt: expiresAt,
		},
		dataDir:       config.DataDir,
		scopes:        append([]string(nil), config.Scopes...),
		execute:       config.Execute,
		now:           now,
		bootstrapHash: sha256.Sum256([]byte(bootstrap)),
		sessionHash:   sha256.Sum256([]byte(session)),
		csrfHash:      sha256.Sum256([]byte(csrf)),
		sessionToken:  session,
		csrfToken:     csrf,
		listener:      listener,
		done:          make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/session", gateway.handleSession)
	mux.HandleFunc("/api/v1/msql", gateway.handleMSQL)
	mux.HandleFunc("/api/", gateway.handleUnknownAPI)
	mux.Handle("/", gateway.shellHandler(shell))
	gateway.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	go gateway.serve()
	timer := time.AfterFunc(ttl, func() { _ = gateway.Close() })
	go func() {
		select {
		case <-ctx.Done():
			_ = gateway.Close()
		case <-gateway.done:
		}
		timer.Stop()
	}()
	return gateway, nil
}

func (gateway *Gateway) Descriptor() Descriptor { return gateway.descriptor }

func (gateway *Gateway) Wait() error {
	<-gateway.done
	gateway.errMu.Lock()
	defer gateway.errMu.Unlock()
	return gateway.serveErr
}

func (gateway *Gateway) Close() error {
	var closeErr error
	gateway.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		closeErr = gateway.server.Shutdown(ctx)
		if closeErr != nil {
			_ = gateway.server.Close()
		}
	})
	return closeErr
}

func (gateway *Gateway) serve() {
	err := gateway.server.Serve(gateway.listener)
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	gateway.errMu.Lock()
	gateway.serveErr = err
	gateway.errMu.Unlock()
	close(gateway.done)
}

func validateListener(listener net.Listener) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		return errors.New("admin API listener is not IPv4 loopback")
	}
	return nil
}

func encodeToken(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}
