package config

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"

	"music-manager/internal/core/safefs"
)

// Config хранит всю конфигурацию сервиса.
type Config struct {
	CensorCloudEnabled      bool
	CensorCloudDailySeconds int
	GeminiAPIKey            string
	GeminiModel             string
	DeepgramAPIKey          string
	DeepgramModel           string

	Host                    string
	Port                    int
	MusicDir                string
	TmpDir                  string
	DatabasePath            string
	SecretKey               string
	AdminLogin              string
	AdminPasswordHash       string
	SessionCookieName       string
	SessionTTL              int
	SessionCookieSecure     bool
	MaxConcurrentDownloads  int
	CookiesFile             string
	YandexMusicToken        string
	YandexLyricsSignKey     string
	YandexMusicProxy        string
	MusicBrainzUserToken    string
	MusicBrainzClientID     string
	MusicBrainzClientSecret string
	MusicBrainzRedirectURI  string
	Debug                   bool

	// Uncensored SoundCloud Resolver
	UncensoredDurationTolerance int
	UncensoredCandidatesLimit   int
	UncensoredPreferYear        int
	UncensoredCacheTTLHours     int
	UncensoredDistributors      []string

	// Radio & Wave Settings
	RadioStreamToken  string
	RadioWaveSize     int
	NavidromeDBPath   string
	NavidromeURL      string
	NavidromeUser     string
	NavidromePassword string
	LastfmAPIKey      string

	// Radar Settings
	RadarScanIntervalHours int
	RadarAutoDownload      bool
}

// Load загружает настройки из .env и переменных окружения, а также проверяет права на запись.
func Load() (*Config, error) {
	// Игнорируем ошибку, если .env файл отсутствует в окружении
	_ = godotenv.Load(".env")
	return loadEnvironment()
}

func loadEnvironment() (*Config, error) {

	port, _ := strconv.Atoi(getEnv("PORT", "8010"))
	sessionTTL, _ := strconv.Atoi(getEnv("SESSION_TTL", "604800"))
	maxDownloads, _ := strconv.Atoi(getEnv("MAX_CONCURRENT_DOWNLOADS", "2"))
	if maxDownloads <= 0 {
		maxDownloads = 2
	}
	cookieSecure := getEnv("SESSION_COOKIE_SECURE", "false") == "true"
	debug := getEnv("DEBUG", "false") == "true"

	musicDir := getEnv("MUSIC_DIR", "music_library")
	tmpDir := getEnv("TMP_DIR", "tmp_downloads")
	dbPath := getEnv("DATABASE_PATH", "data/library.db")

	uncensoredTolerance, _ := strconv.Atoi(getEnv("UNCENSORED_DURATION_TOLERANCE", "2"))
	uncensoredLimit, _ := strconv.Atoi(getEnv("UNCENSORED_CANDIDATES_LIMIT", "10"))
	uncensoredPreferYear, _ := strconv.Atoi(getEnv("UNCENSORED_PREFER_YEAR", "2023"))
	uncensoredTTLHours, _ := strconv.Atoi(getEnv("UNCENSORED_CACHE_TTL_HOURS", "720"))
	distributorsRaw := getEnv("UNCENSORED_DISTRIBUTORS", "distrokid,believe,zvonko,the orchard,tunecore,warner,universal,sony")
	var distributors []string
	for _, d := range strings.Split(distributorsRaw, ",") {
		if trimmed := strings.TrimSpace(d); trimmed != "" {
			distributors = append(distributors, strings.ToLower(trimmed))
		}
	}

	radioToken := getEnv("RADIO_STREAM_TOKEN", "")
	radioWaveSize, _ := strconv.Atoi(getEnv("RADIO_WAVE_SIZE", "60"))
	if radioWaveSize <= 0 {
		radioWaveSize = 60
	}
	radarIntervalHours, _ := strconv.Atoi(getEnv("RADAR_SCAN_INTERVAL_HOURS", "24"))
	if radarIntervalHours <= 0 {
		radarIntervalHours = 24
	}
	radarAutoDL := getEnv("RADAR_AUTO_DOWNLOAD", "false") == "true"

	censorSeconds, _ := strconv.Atoi(getEnv("CENSOR_CLOUD_DAILY_SECONDS", "0"))
	cfg := &Config{
		CensorCloudEnabled:          getEnv("CENSOR_CLOUD_ENABLED", "false") == "true",
		CensorCloudDailySeconds:     censorSeconds,
		GeminiAPIKey:                getEnv("GEMINI_API_KEY", ""),
		GeminiModel:                 getEnv("GEMINI_MODEL", ""),
		DeepgramAPIKey:              getEnv("DEEPGRAM_API_KEY", ""),
		DeepgramModel:               getEnv("DEEPGRAM_MODEL", ""),
		Host:                        getEnv("HOST", "127.0.0.1"),
		Port:                        port,
		MusicDir:                    musicDir,
		TmpDir:                      tmpDir,
		DatabasePath:                dbPath,
		SecretKey:                   getEnv("SECRET_KEY", ""),
		AdminLogin:                  getEnv("ADMIN_LOGIN", "admin"),
		AdminPasswordHash:           getEnv("ADMIN_PASSWORD_HASH", ""),
		SessionCookieName:           getEnv("SESSION_COOKIE_NAME", "music_manager_session"),
		SessionTTL:                  sessionTTL,
		SessionCookieSecure:         cookieSecure,
		MaxConcurrentDownloads:      maxDownloads,
		CookiesFile:                 getEnv("YTDLP_COOKIES_PATH", "cookies.txt"),
		YandexMusicToken:            getEnv("YANDEX_MUSIC_TOKEN", ""),
		YandexLyricsSignKey:         getEnv("YANDEX_LYRICS_SIGN_KEY", ""),
		YandexMusicProxy:            getEnv("YANDEX_MUSIC_PROXY", ""),
		MusicBrainzUserToken:        getEnv("MUSICBRAINZ_USER_TOKEN", ""),
		MusicBrainzClientID:         getEnv("MUSICBRAINZ_CLIENT_ID", ""),
		MusicBrainzClientSecret:     getEnv("MUSICBRAINZ_CLIENT_SECRET", ""),
		MusicBrainzRedirectURI:      getEnv("MUSICBRAINZ_REDIRECT_URI", ""),
		Debug:                       debug,
		UncensoredDurationTolerance: uncensoredTolerance,
		UncensoredCandidatesLimit:   uncensoredLimit,
		UncensoredPreferYear:        uncensoredPreferYear,
		UncensoredCacheTTLHours:     uncensoredTTLHours,
		UncensoredDistributors:      distributors,
		RadioStreamToken:            radioToken,
		RadioWaveSize:               radioWaveSize,
		NavidromeDBPath:             getEnv("NAVIDROME_DB_PATH", ""),
		NavidromeURL:                getEnv("NAVIDROME_URL", ""),
		NavidromeUser:               getEnv("NAVIDROME_USER", ""),
		NavidromePassword:           getEnv("NAVIDROME_PASSWORD", ""),
		LastfmAPIKey:                getEnv("LASTFM_API_KEY", ""),
		RadarScanIntervalHours:      radarIntervalHours,
		RadarAutoDownload:           radarAutoDL,
	}

	if len(cfg.SecretKey) < 32 || strings.HasPrefix(cfg.SecretKey, "change_me") || cfg.AdminPasswordHash == "" {
		return nil, fmt.Errorf("SECRET_KEY (at least 32 characters) and ADMIN_PASSWORD_HASH are required")
	}
	if _, err := bcrypt.Cost([]byte(cfg.AdminPasswordHash)); err != nil {
		return nil, fmt.Errorf("invalid ADMIN_PASSWORD_HASH: %w", err)
	}
	if cfg.SessionTTL <= 0 {
		return nil, fmt.Errorf("SESSION_TTL must be positive")
	}
	if err := (&http.Cookie{Name: cfg.SessionCookieName, Value: "probe"}).Valid(); err != nil {
		return nil, fmt.Errorf("invalid SESSION_COOKIE_NAME: %w", err)
	}
	// Fail-fast проверка прав на запись в директории
	if err := verifyStorageDirectory(cfg.MusicDir, "MUSIC_DIR"); err != nil {
		return nil, err
	}
	if err := verifyStorageDirectory(cfg.TmpDir, "TMP_DIR"); err != nil {
		return nil, err
	}

	// Создаем родительскую папку для базы данных, если нужно
	dbDir := filepath.Dir(cfg.DatabasePath)
	if dbDir != "" && dbDir != "." {
		_ = safefs.MkdirAll(dbDir, 0755)
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return fallback
	}
	return val
}

func verifyStorageDirectory(dirPath, name string) error {
	if err := safefs.MkdirAll(dirPath, 0755); err != nil {
		return fmt.Errorf("каталог %s ('%s') не может быть создан: %w", name, dirPath, err)
	}

	probeFile := filepath.Join(dirPath, fmt.Sprintf(".probe_%s.tmp", uuid.New().String()))
	if err := safefs.WriteFile(probeFile, []byte("probe_rw_test"), 0600); err != nil {
		return fmt.Errorf("каталог %s ('%s') недоступен для записи: %w", name, dirPath, err)
	}
	_ = safefs.Remove(probeFile)

	return nil
}
