package registry

import (
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/google/go-containerregistry/pkg/registry"
)

// Server represents an active ephemeral OCI registry server.
type Server struct {
	server *httptest.Server
	URL    string
}

// Option configures a Server during construction.
type Option func(*serverConfig)

type serverConfig struct {
	wrapper     func(http.Handler) http.Handler
	blobHandler registry.BlobHandler
}

// WithHandlerWrapper wraps the registry's HTTP handler (e.g., for request counting or logging).
func WithHandlerWrapper(wrapper func(http.Handler) http.Handler) Option {
	return func(cfg *serverConfig) {
		cfg.wrapper = wrapper
	}
}

// WithBlobHandler configures the blob storage backend for the registry.
func WithBlobHandler(bh registry.BlobHandler) Option {
	return func(cfg *serverConfig) {
		cfg.blobHandler = bh
	}
}

// NewServer constructs and starts a new ephemeral in-memory OCI registry.
func NewServer(opts ...Option) (*Server, error) {
	var cfg serverConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	var regOpts []registry.Option
	if cfg.blobHandler != nil {
		regOpts = append(regOpts, registry.WithBlobHandler(cfg.blobHandler))
	}

	var handler http.Handler = registry.New(regOpts...)
	if cfg.wrapper != nil {
		handler = cfg.wrapper(handler)
	}

	ts := httptest.NewServer(handler)
	return &Server{
		server: ts,
		URL:    ts.URL,
	}, nil
}

// Addr returns the host:port string of the server (e.g. 127.0.0.1:54321).
func (s *Server) Addr() string {
	if s == nil || s.server == nil {
		return ""
	}
	return s.server.Listener.Addr().String()
}

// Host returns the host and port without protocol prefix (suitable for docker repo refs).
func (s *Server) Host() string {
	return strings.TrimPrefix(s.URL, "http://")
}

// Repo constructs a repository reference string for this test registry (e.g., "127.0.0.1:54321/my-repo").
func (s *Server) Repo(name string) string {
	return s.Host() + "/" + strings.TrimPrefix(name, "/")
}

// HTTPServer returns the underlying httptest.Server.
func (s *Server) HTTPServer() *httptest.Server {
	if s == nil {
		return nil
	}
	return s.server
}

// Close shuts down the test registry server.
func (s *Server) Close() {
	if s != nil && s.server != nil {
		s.server.Close()
	}
}
