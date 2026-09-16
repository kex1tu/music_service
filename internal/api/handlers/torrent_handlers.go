package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
)

// UploadTorrent handles .torrent file upload and responds with informative message.
func (h *DownloadHandler) UploadTorrent(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Limit upload size to 10MB
	r.Body = http.MaxBytesReader(w, r.Body, 10<<20)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Превышен размер файла или неверный формат формы"})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Файл .torrent не передан"})
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".torrent") {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Допускаются только файлы с расширением .torrent"})
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":  "error",
		"message": "Загрузка через .torrent временно недоступна в текущей версии. Пожалуйста, используйте ссылки Deezer, YouTube, Spotify или Яндекс Музыки.",
	})
}
