package radio

import (
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"music-manager/internal/core/sandbox"
	"music-manager/internal/db"
)

const (
	ICYMetaInt   = 16384  // Стандартный интервал внедрения ICY-метаданных в байтах
	BitrateBytes = 40000  // 320 kbps = 40 000 байт/сек
	BurstBytes   = 320000 // 8 секунд буфера = 320 КБ для мгновенного и надежного старта плееров
	MiniBurst    = 40000  // 1 секунда буфера при смене трека для компенсации задержки селектора/диска
	TickChunk    = 4000   // 4 000 байт за каждые 100 мс (40 000 байт/сек)
)

// IcyStreamer осуществляет непрерывное вещание по протоколу HTTP ICY с динамической сменой метаданных.
type IcyStreamer struct {
	selector *WaveSelector
	sandbox  *sandbox.PathSandbox
	database *db.DB
	token    string
}

// NewIcyStreamer создает новый экземпляр IcyStreamer.
func NewIcyStreamer(selector *WaveSelector, sb *sandbox.PathSandbox, database *db.DB, token string) *IcyStreamer {
	return &IcyStreamer{
		selector: selector,
		sandbox:  sb,
		database: database,
		token:    token,
	}
}

// ValidateToken проверяет валидность токена стриминга.
func (s *IcyStreamer) ValidateToken(token string) bool {
	if s.token == "" {
		return true // Если токен не задан в конфиге, вещание открыто
	}
	return strings.TrimSpace(token) == strings.TrimSpace(s.token)
}

// ServeHTTP обрабатывает HTTP-запрос стриминга радиостанции.
func (s *IcyStreamer) ServeHTTP(w http.ResponseWriter, r *http.Request, stationType, stationParam string) {
	// 1. Установка глобальных CORS-заголовков (необходимы для Navidrome, внешних плееров и Web Audio API)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Content-Type, icy-metaint, icy-name, icy-genre, icy-br")

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// 2. Валидация токена (если передан в запросе).
	// Авторизация сессионных cookie и Bearer-токенов выполняется выше в RadioHandler.
	reqToken := r.URL.Query().Get("token")
	if reqToken == "" {
		reqToken = r.Header.Get("X-Stream-Token")
	}
	if reqToken != "" && !s.ValidateToken(reqToken) {
		http.Error(w, "Unauthorized: invalid radio stream token", http.StatusUnauthorized)
		return
	}

	// 3. Выбираем ПЕРВЫЙ трек до отправки заголовков 200 OK
	firstTrack, err := s.selector.SelectNextTrack(r.Context(), stationType, stationParam)
	if err != nil || firstTrack == nil {
		http.Error(w, "No tracks available in library for radio stream", http.StatusNotFound)
		return
	}

	// 4. Проверка поддержки ICY-метаданных клиентом
	hasICYMeta := strings.EqualFold(r.Header.Get("Icy-MetaData"), "1")

	// 5. Установка HTTP-заголовков вещания
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("icy-name", "Моя Волна | Music Manager")
	w.Header().Set("icy-genre", "Personal Radio")
	w.Header().Set("icy-br", "320")

	if hasICYMeta {
		w.Header().Set("icy-metaint", strconv.Itoa(ICYMetaInt))
	}

	// Отключаем WriteDeadline для бесконечного стриминга (Go 1.20+)
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	flusher, hasFlusher := w.(http.Flusher)
	if hasFlusher {
		flusher.Flush()
	}

	log.Printf("[Radio] Client connected from %s (station: %s, ICY-meta: %v)", r.RemoteAddr, stationType, hasICYMeta)
	defer log.Printf("[Radio] Client disconnected: %s", r.RemoteAddr)

	burstRemaining := BurstBytes
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	bytesUntilMeta := ICYMetaInt
	currentTitle := ""
	lastSentTitle := ""

	nextTrack := firstTrack

	// 6. Бесконечный цикл вещания треков
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		track := nextTrack
		if track == nil {
			var selErr error
			track, selErr = s.selector.SelectNextTrack(r.Context(), stationType, stationParam)
			if selErr != nil || track == nil {
				time.Sleep(500 * time.Millisecond)
				continue
			}
		}
		nextTrack = nil // Сбрасываем для следующей итерации

		// Безопасное открытие и подготовка файла (пропуск ID3v2, поиск syncword, обрезка ID3v1)
		f, audioLimit, err := s.prepareTrackFile(track.FilePath)
		if err != nil {
			log.Printf("[Radio] Failed to open track %q: %v", track.FilePath, err)
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// Формирование строки метаданных
		displayTitle := fmt.Sprintf("%s - %s", track.Artist, track.Title)
		currentTitle = displayTitle
		log.Printf("[Radio] Now streaming: %s", displayTitle)

		// Фиксация воспроизведения в БД для умной ротации
		if s.database != nil {
			_ = s.database.RecordRadioPlay(r.Context(), track.ID, stationType, stationParam)
		}

		// Потоковая передача аудиоданных трека
		buf := make([]byte, TickChunk)
		var bytesReadTotal int64

		// Даем мини-буфер при смене трека, чтобы плеер не испытывал голодания
		if burstRemaining < MiniBurst {
			burstRemaining = MiniBurst
		}

		trackEnded := false
		for !trackEnded {
			select {
			case <-r.Context().Done():
				f.Close()
				return
			default:
			}

			// Pacing / Burst управление скоростью
			if burstRemaining > 0 {
				burstRemaining -= TickChunk
			} else {
				select {
				case <-ticker.C:
				case <-r.Context().Done():
					f.Close()
					return
				}
			}

			// Определяем, сколько байт читать до достижения audioLimit (без ID3v1)
			toRead := len(buf)
			if audioLimit > 0 {
				remain := audioLimit - bytesReadTotal
				if remain <= 0 {
					trackEnded = true
					break
				}
				if int64(toRead) > remain {
					toRead = int(remain)
				}
			}

			n, readErr := f.Read(buf[:toRead])
			if n > 0 {
				bytesReadTotal += int64(n)
				dataToSend := buf[:n]

				if !hasICYMeta {
					// Клиент не поддерживает ICY: отправляем чистый аудиопоток
					if _, writeErr := w.Write(dataToSend); writeErr != nil {
						f.Close()
						return
					}
					if hasFlusher {
						flusher.Flush()
					}
				} else {
					// Клиент запросил ICY-метаданные: инжекция с шагом ICYMetaInt
					for len(dataToSend) > 0 {
						chunkLen := len(dataToSend)
						if chunkLen > bytesUntilMeta {
							chunkLen = bytesUntilMeta
						}

						if _, writeErr := w.Write(dataToSend[:chunkLen]); writeErr != nil {
							f.Close()
							return
						}
						bytesUntilMeta -= chunkLen
						dataToSend = dataToSend[chunkLen:]

						if bytesUntilMeta == 0 {
							if currentTitle != lastSentTitle {
								metaBlock := buildICYMetadataBlock(currentTitle)
								if _, writeErr := w.Write(metaBlock); writeErr != nil {
									f.Close()
									return
								}
								lastSentTitle = currentTitle
							} else {
								// Если название не менялось, по стандарту ICY передается 1 нулевой байт
								if _, writeErr := w.Write([]byte{0}); writeErr != nil {
									f.Close()
									return
								}
							}
							bytesUntilMeta = ICYMetaInt
						}
					}
					if hasFlusher {
						flusher.Flush()
					}
				}
			}

			if readErr != nil {
				if readErr == io.EOF {
					trackEnded = true
				} else {
					f.Close()
					return
				}
			}
		}

		f.Close()
	}
}

// prepareTrackFile безопасно открывает файл трека через PathSandbox, пропускает заголовок ID3v2,
// выравнивает смещение на первый валидный MPEG-аудиофрейм (0xFF) и отсекает ID3v1 в конце файла.
func (s *IcyStreamer) prepareTrackFile(filePath string) (*os.File, int64, error) {
	absPath, err := s.sandbox.Resolve(filePath, false)
	if err != nil {
		return nil, 0, err
	}

	f, err := os.Open(absPath)
	if err != nil {
		return nil, 0, err
	}

	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	fileSize := stat.Size()
	audioLimit := fileSize

	// Проверяем наличие ID3v1 в конце файла (последние 128 байт начинаются с "TAG")
	if fileSize > 128 {
		trailer := make([]byte, 3)
		if _, err := f.ReadAt(trailer, fileSize-128); err == nil {
			if trailer[0] == 'T' && trailer[1] == 'A' && trailer[2] == 'G' {
				audioLimit = fileSize - 128
			}
		}
	}

	// Считываем и пропускаем ID3v2 заголовок, если он есть
	header := make([]byte, 10)
	n, err := f.Read(header)
	if err == nil && n == 10 && header[0] == 'I' && header[1] == 'D' && header[2] == '3' {
		// Syncsafe integer: размер тела ID3v2 в байтах 6..9
		tagSize := (int64(header[6]&0x7F) << 21) |
			(int64(header[7]&0x7F) << 14) |
			(int64(header[8]&0x7F) << 7) |
			int64(header[9]&0x7F)

		totalSkip := 10 + tagSize
		if header[5]&0x10 != 0 {
			totalSkip += 10 // Footer
		}
		_, _ = f.Seek(totalSkip, io.SeekStart)
	} else {
		_, _ = f.Seek(0, io.SeekStart)
	}

	// Ищем первый байт синхронизации MPEG (0xFF seguido de 0xE0..0xFF)
	// чтобы пропустить возможный паддинг нулей между тегами и фреймами
	currPos, _ := f.Seek(0, io.SeekCurrent)
	syncBuf := make([]byte, 4096)
	nSync, _ := f.Read(syncBuf)
	for i := 0; i < nSync-1; i++ {
		if syncBuf[i] == 0xFF && (syncBuf[i+1]&0xE0) == 0xE0 {
			_, _ = f.Seek(currPos+int64(i), io.SeekStart)
			break
		}
	}

	return f, audioLimit, nil
}

// buildICYMetadataBlock формирует байтовый блок метаданных по спецификации ICY/Shoutcast.
func buildICYMetadataBlock(streamTitle string) []byte {
	if streamTitle == "" {
		return []byte{0} // Длина 0 = блок из 1 нулевого байта
	}

	metaPayload := fmt.Sprintf("StreamTitle='%s';", streamTitle)
	payloadBytes := []byte(metaPayload)

	// Количество 16-байтовых блоков (округление вверх)
	numBlocks := int(math.Ceil(float64(len(payloadBytes)) / 16.0))
	if numBlocks > 255 {
		numBlocks = 255
	}
	totalLength := numBlocks * 16

	block := make([]byte, 1+totalLength)
	block[0] = byte(numBlocks)
	copy(block[1:], payloadBytes)
	// Оставшиеся байты блока заполнены нулями (\0)
	return block
}
