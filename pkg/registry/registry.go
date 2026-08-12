// Package registry provides an ephemeral, in-memory OCI 1.1 compliant registry
// helper for integration tests and local development workflows.
package registry

import (
	"net/http/httptest"
	"strings"

	"github.com/google/go-containerregistry/pkg/registry"
)

// Server represents an active ephemeral OCI registry server.
type Server struct {
	server *httptest.Server
	URL    string
}

// NewServer constructs and starts a new ephemeral in-memory OCI registry.
func NewServer() (*Server, error) {
	regHandler := registry.New()
	ts := httptest.NewServer(regHandler)
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

// Close shuts down the test registry server.
func (s *Server) Close() {
	if s != nil && s.server != nil {
		s.server.Close()
	}
}
