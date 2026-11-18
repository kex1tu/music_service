package handlers

import (
	"net/http"

	"music-manager/internal/features/audit"
)

func (h *FileHandler) duplicateModule() (*audit.Module, func() error) {
	service := audit.NewService(audit.ServiceConfig{
		MusicSandbox: h.musicSandbox,
		Repo:         audit.NewRepository(h.database.Conn()),
	})
	return audit.NewModule(service), service.Close
}

func (h *FileHandler) Duplicates(w http.ResponseWriter, r *http.Request) {
	module, closeService := h.duplicateModule()
	defer closeService()
	module.Duplicates(w, r)
}

func (h *FileHandler) FixDuplicates(w http.ResponseWriter, r *http.Request) {
	module, closeService := h.duplicateModule()
	defer closeService()
	module.FixDuplicates(w, r)
}

func (h *FileHandler) FixAllDuplicates(w http.ResponseWriter, r *http.Request) {
	module, closeService := h.duplicateModule()
	defer closeService()
	module.FixAllDuplicates(w, r)
}
