package http

import (
	"net/http"
	"strings"
)

type HandlerMultiplexer struct {
	HandlerMap map[string]http.Handler
}

func (h *HandlerMultiplexer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parentPath := strings.Split(strings.TrimLeft(r.URL.Path, "/"), "/")[0]

	if handler, ok := h.HandlerMap[parentPath]; ok {
		handler.ServeHTTP(w, r)
	}
}
