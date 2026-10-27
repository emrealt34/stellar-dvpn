package api

import "net/http"

// agentFetch wraps the metered GET service.
func (s *Server) agentFetch(w http.ResponseWriter, r *http.Request) {
	s.Agent.FetchHandler(w, r)
}

// agentConnect wraps the metered CONNECT proxy.
func (s *Server) agentConnect(w http.ResponseWriter, r *http.Request) {
	s.Agent.ConnectHandler(w, r)
}
