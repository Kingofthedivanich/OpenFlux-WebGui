package panel

import (
	"net/http"

	"openflux/clientqr"
)

func (s *Server) handleClientQR(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status, ok := s.mgr.Get(id)
	if !ok {
		writeJSONError(w, http.StatusNotFound, "client not found")
		return
	}

	tun, err := clientqr.Build(status, s.publicKey)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	png, err := clientqr.PNG(tun, 512)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}
