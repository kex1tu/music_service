package radio

import (
	"fmt"
	"io"
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
	ICYMetaInt   = 16384 // Стандартный интервал внедрения ICY-метаданных в байтах
	BitrateBytes = 40000 // 320 kbps = 40 000 байт/сек
	BurstBytes   = 120000 // 3 секунды буфера = 120 КБ для мгновенного старта
	TickChunk    = 4000  // 4 000 байт за каждые 100 мс (40 000 байт/сек)
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
	// 1. Проверка авторизации: query-токен ?token=...
	reqToken := r.URL.Query().Get("token")
	if reqToken == "" {
		reqToken = r.Header.Get("X-Stream-Token")
	}

	if !s.ValidateToken(reqToken) {
		http.Error(w, "Unauthorized: invalid or missing radio stream token", http.StatusUnauthorized)
		return
	}

	// 2. Проверка поддержки ICY-метаданных клиентом
	hasICYMeta := strings.EqualFold(r.Header.Get("Icy-MetaData"), "1")

	// 3. Установка HTTP-заголовков вещания
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("icy-name", "Моя Волна | Music Manager")
	w.Header().Set("icy-genre", "Personal Radio")
	w.Header().Set("icy-br", "320")

	if hasICYMeta {
		w.Header().Set("icy-metaint", strconv.Itoa(ICYMetaInt))
	}

	flusher, hasFlusher := w.(http.Flusher)
	if hasFlusher {
		flusher.Flush()
	}

	burstRemaining := BurstBytes
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	bytesUntilMeta := ICYMetaInt
	currentTitle := ""

	// 4. Бесконечный цикл вещания треков
	for {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		// Выбор очередного трека
		track, err := s.selector.SelectNextTrack(r.Context(), stationType, stationParam)
		if err != nil || track == nil {
			time.Sleep(1 * time.Second)
			continue
		}

		// Безопасное открытие файла через PathSandbox
		absPath, err := s.sandbox.Resolve(track.FilePath, false)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		f, err := os.Open(absPath)
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}

		// Zero-Transcoding: пропуск стартового заголовка ID3v2 для передачи чистых MPEG фреймов
		s.skipID3v2Header(f)

		// Формирование строки метаданных
		displayTitle := fmt.Sprintf("%s - %s", track.Artist, track.Title)
		currentTitle = displayTitle

		// Фиксация воспроизведения в БД для ротации
		if s.database != nil {
			_ = s.database.RecordRadioPlay(r.Context(), track.ID, stationType, stationParam)
		}

		// Потоковая передача аудиоданных трека
		buf := make([]byte, TickChunk)
		eof := false

		for !eof {
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

			n, readErr := f.Read(buf)
			if n > 0 {
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
					// Клиент запросил ICY-метаданные: производим инжекцию с шагом ICYMetaInt
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
							// Инжекция метаданных
							metaBlock := buildICYMetadataBlock(currentTitle)
							if _, writeErr := w.Write(metaBlock); writeErr != nil {
								f.Close()
								return
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
					eof = true
				} else {
					f.Close()
					return
				}
			}
		}

		f.Close()
	}
}

// skipID3v2Header считывает заголовок ID3v2 и пропускает его в файле, смещая указатель на первый MPEG audio фрейм.
func (s *IcyStreamer) skipID3v2Header(f *os.File) {
	header := make([]byte, 10)
	n, err := f.Read(header)
	if err != nil || n < 10 {
		_, _ = f.Seek(0, io.SeekStart)
		return
	}

	if header[0] == 'I' && header[1] == 'D' && header[2] == '3' {
		// Syncsafe integer: размер тела ID3v2 в байтах 6..9
		tagSize := (int64(header[6]&0x7F) << 21) |
			(int64(header[7]&0x7F) << 14) |
			(int64(header[8]&0x7F) << 7) |
			int64(header[9]&0x7F)

		totalSkip := 10 + tagSize
		// Если установлен флаг footer (бит 4 в flags header[5]), добавляется еще 10 байт
		if header[5]&0x10 != 0 {
			totalSkip += 10
		}
		_, _ = f.Seek(totalSkip, io.SeekStart)
	} else {
		// Если ID3v2 заголовок отсутствует, возвращаемся в самое начало
		_, _ = f.Seek(0, io.SeekStart)
	}
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
