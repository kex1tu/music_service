#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
detect_censorship.py — Autonomous CLI Audio Censorship Detector.

Features:
  - Universal path argument: file (detailed per-word breakdown) or folder (batch scan with summary table).
  - Fast trigger lexicon scan: clean tracks skipped in ~0.01s.
  - Contextual 10-second slice analysis [T - 5.0s, T + 5.0s] around suspect words.
  - Mandatory Blind ASR (faster-whisper / whisper.cpp) without prompt bias.
  - Multi-defect acoustic DSP:
      1. Air/Voice Energy Ratio (> 3.0) + Pitch F0 absence -> CENSORED_EXHALE
      2. Vocal Ducking / Mid Channel Mute Drop (> 14 dB) -> CENSORED_MUTE
      3. Narrow-band 1 kHz Pure Sine Tone (Q > 25) -> CENSORED_BLEEP
      4. Euphemism / Clean Substitution Detection -> CENSORED_SUBSTITUTION
  - Formats: Rich CLI terminal output or structured JSON (--json).
"""

import os
import sys

# Обеспечиваем немедленный сброс вывода строк в stdout / stderr
try:
    sys.stdout.reconfigure(line_buffering=True)
    sys.stderr.reconfigure(line_buffering=True)
except Exception:
    pass

import re
import json
import time
import shutil
import argparse
import tempfile
import subprocess
import urllib.request
import urllib.parse
from pathlib import Path
from typing import List, Dict, Any, Optional, Tuple

import numpy as np

# Импорт лексикона
try:
    from scripts.lexicon_data import (
        classify_word,
        find_triggers_in_line,
        DRUG_WORDS_SET,
        PROFANITY_EXACT_SET,
        SUSPECT_MARKERS_SET,
        CENSORSHIP_SUSPECT_MARKERS
    )
except ImportError:
    from lexicon_data import (
        classify_word,
        find_triggers_in_line,
        DRUG_WORDS_SET,
        PROFANITY_EXACT_SET,
        SUSPECT_MARKERS_SET,
        CENSORSHIP_SUSPECT_MARKERS
    )

AUDIO_EXTENSIONS = {'.mp3', '.flac', '.wav', '.m4a', '.ogg', '.aac', '.opus', '.wma'}
LRC_TIME_RE = re.compile(r"\[(\d{1,2}):(\d{2}(?:\.\d+)?)\](.*)")


def format_time(seconds: float) -> str:
    """Форматирует секунды в MM:SS.xx"""
    if seconds < 0:
        seconds = 0
    m = int(seconds // 60)
    s = seconds % 60
    return f"{m:02d}:{s:05.2f}"


def format_duration(seconds: float) -> str:
    """Форматирует длительность в MM:SS"""
    m = int(seconds // 60)
    s = int(seconds % 60)
    return f"{m:02d}:{s:02d}"


def format_energy(val: float) -> str:
    """Форматирует число энергии в удобочитаемый вид (M / k)"""
    if val >= 1e6:
        return f"{val / 1e6:.1f}M"
    elif val >= 1e3:
        return f"{val / 1e3:.1f}k"
    else:
        return f"{val:.1f}"


# ==============================================================================
# 1. МЕТАДАННЫЕ И ТЕКСТЫ ПЕСЕН
# ==============================================================================

def get_audio_info(file_path: Path) -> Dict[str, Any]:
    """Извлекает метаданные и длительность трека через ffprobe"""
    cmd = [
        "ffprobe", "-v", "quiet", "-print_format", "json",
        "-show_format", "-show_streams", str(file_path)
    ]
    try:
        res = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=True)
        data = json.loads(res.stdout)
        fmt = data.get("format", {})
        tags = fmt.get("tags", {})
        
        # Поиск тегов независимо от регистра
        lower_tags = {k.lower(): v for k, v in tags.items()}
        
        duration = float(fmt.get("duration", 0.0))
        artist = lower_tags.get("artist") or lower_tags.get("album_artist") or "Unknown Artist"
        title = lower_tags.get("title") or file_path.stem
        album = lower_tags.get("album") or "Unknown Album"
        
        return {
            "path": str(file_path),
            "filename": file_path.name,
            "duration": duration,
            "artist": artist,
            "title": title,
            "album": album,
            "tags": lower_tags
        }
    except Exception as e:
        return {
            "path": str(file_path),
            "filename": file_path.name,
            "duration": 0.0,
            "artist": "Unknown Artist",
            "title": file_path.stem,
            "album": "Unknown Album",
            "tags": {},
            "error": str(e)
        }


def parse_lrc_content(content: str) -> List[Dict[str, Any]]:
    """Парсит синхронизированный LRC файл на строки с таймкодами"""
    lines = []
    for raw_line in content.splitlines():
        raw_line = raw_line.strip()
        match = LRC_TIME_RE.match(raw_line)
        if match:
            min_str, sec_str, text = match.groups()
            t = int(min_str) * 60.0 + float(sec_str)
            text_clean = text.strip()
            if text_clean:
                lines.append({"time": t, "text": text_clean})
    lines.sort(key=lambda x: x["time"])
    return lines


def find_lyrics_source(audio_path: Path, meta: Dict[str, Any]) -> Tuple[str, List[Dict[str, Any]], str]:
    """
    Ищет источник текста для трека:
      1. Рядом лежащий .lrc файл
      2. Рядом лежащий .txt файл
      3. Онлайн LRCLIB (если включено)
    Возвращает (source_name, parsed_lines, raw_text).
    """
    # 1. Проверяем локальный .lrc
    lrc_path = audio_path.with_suffix(".lrc")
    if lrc_path.is_file():
        try:
            content = lrc_path.read_text(encoding="utf-8", errors="replace")
            parsed = parse_lrc_content(content)
            if parsed:
                return "LRC (Local File)", parsed, content
        except Exception:
            pass

    # Проверяем, может быть .lrc в родительской папке или соседних директориях альбома
    for alt_lrc in audio_path.parent.glob("*.lrc"):
        if alt_lrc.stem.lower() == audio_path.stem.lower():
            try:
                content = alt_lrc.read_text(encoding="utf-8", errors="replace")
                parsed = parse_lrc_content(content)
                if parsed:
                    return f"LRC ({alt_lrc.name})", parsed, content
            except Exception:
                pass

    # 2. Проверяем локальный .txt
    txt_path = audio_path.with_suffix(".txt")
    if txt_path.is_file():
        try:
            content = txt_path.read_text(encoding="utf-8", errors="replace")
            plain_lines = []
            for line in content.splitlines():
                line = line.strip()
                if line and not line.startswith("[") and not line.startswith("http"):
                    plain_lines.append({"time": -1.0, "text": line})
            if plain_lines:
                return "TXT (Local File)", plain_lines, content
        except Exception:
            pass

    # 3. Онлайн поиск в LRCLIB
    artist = meta.get("artist")
    title = meta.get("title")
    duration = int(meta.get("duration", 0))
    if artist and title and artist != "Unknown Artist":
        try:
            url = (
                f"https://lrclib.net/api/get?artist_name={urllib.parse.quote(artist)}"
                f"&track_name={urllib.parse.quote(title)}"
            )
            if duration > 0:
                url += f"&duration={duration}"
            req = urllib.request.Request(url, headers={"User-Agent": "MusicCensorshipDetector/1.0"})
            with urllib.request.urlopen(req, timeout=3.0) as resp:
                data = json.loads(resp.read().decode("utf-8"))
                synced = data.get("syncedLyrics")
                if synced:
                    parsed = parse_lrc_content(synced)
                    if parsed:
                        return "LRCLIB (Synced)", parsed, synced
                plain = data.get("plainLyrics")
                if plain:
                    plain_lines = [{"time": -1.0, "text": l.strip()} for l in plain.splitlines() if l.strip()]
                    return "LRCLIB (Plain)", plain_lines, plain
        except Exception:
            pass

    return "NONE", [], ""


# ==============================================================================
# 2. DSP ENGINE (Air/Voice Ratio, Mid Mute, 1 kHz Bleep)
# ==============================================================================

def analyze_dsp_features(
    mono_audio: np.ndarray,
    sample_rate: int,
    target_rel_time: float,
    search_window: float = 1.0
) -> Dict[str, Any]:
    """
    Вычисляет спектральные и динамические DSP-метрики в окрестности целевого времени:
      - Air/Voice Ratio (энергия 2-7 кГц к 0.2-1.2 кГц)
      - F0 Pitch (наличие основного тона гласной через автокорреляцию)
      - Mid Mute Drop (вокальный провал в dB)
      - 1 kHz Sine Tone (бип цензуры)
    """
    total_len = len(mono_audio)
    if total_len == 0 or sample_rate <= 0:
        return {
            "air_voice_ratio": 0.0,
            "air_time": target_rel_time,
            "air_energy": 0.0,
            "voice_energy": 0.0,
            "pitch_f0": False,
            "mid_mute_drop_db": 0.0,
            "bleep_detected": False
        }

    t_start = max(0.0, target_rel_time - search_window / 2)
    t_end = min(total_len / sample_rate, target_rel_time + search_window / 2)

    idx_start = int(t_start * sample_rate)
    idx_end = int(t_end * sample_rate)

    chunk_len = int(sample_rate * 0.1)  # 100 ms окно
    step = int(sample_rate * 0.05)      # 50 ms шаг

    peak_air_voice_ratio = 0.0
    peak_air_time = target_rel_time
    peak_air_energy = 0.0
    peak_voice_energy = 0.0
    pitch_f0_at_peak = False

    min_lag = int(sample_rate / 350)  # ~70 - 350 Hz vocal range
    max_lag = int(sample_rate / 70)

    # 1. Спектральный анализ (Air vs Voice)
    hanning_win = np.hanning(chunk_len)
    for i in range(idx_start, max(idx_start + 1, idx_end - chunk_len), step):
        chunk = mono_audio[i:i + chunk_len]
        if len(chunk) < chunk_len:
            break
        chunk_t = (i + chunk_len / 2) / sample_rate

        fft = np.abs(np.fft.rfft(chunk * hanning_win))
        freqs = np.fft.rfftfreq(len(chunk), 1.0 / sample_rate)

        voice_e = float(np.sum(fft[(freqs >= 200) & (freqs <= 1200)]))
        air_e = float(np.sum(fft[(freqs >= 2000) & (freqs <= 7000)]))
        ratio = air_e / (voice_e + 1e-6)

        if ratio > peak_air_voice_ratio:
            peak_air_voice_ratio = ratio
            peak_air_time = chunk_t
            peak_air_energy = air_e
            peak_voice_energy = voice_e

            # Проверка F0 через автокорреляцию
            norm_chunk = chunk - np.mean(chunk)
            var = np.sum(norm_chunk ** 2)
            if var > 1e-5:
                autocorr = np.correlate(norm_chunk, norm_chunk, mode='full')
                autocorr = autocorr[len(norm_chunk) - 1:]
                autocorr_norm = autocorr / (var + 1e-6)
                if max_lag <= len(autocorr_norm):
                    r_max = float(np.max(autocorr_norm[min_lag:max_lag]))
                    pitch_f0_at_peak = r_max > 0.35
                else:
                    pitch_f0_at_peak = False
            else:
                pitch_f0_at_peak = False

    # 2. Детектор Mid Mute Drop (вокального провала)
    total_rms = float(np.sqrt(np.mean(mono_audio ** 2))) + 1e-6
    target_segment = mono_audio[idx_start:idx_end]
    min_rms = total_rms
    if len(target_segment) >= chunk_len:
        for i in range(0, len(target_segment) - chunk_len, step):
            sub = target_segment[i:i + chunk_len]
            rms = float(np.sqrt(np.mean(sub ** 2)))
            if rms < min_rms:
                min_rms = rms
    drop_db = max(0.0, float(20 * np.log10(total_rms / (min_rms + 1e-6))))

    # 3. Детектор бипа (1 kHz Sine Bleep)
    bleep_detected = False
    for i in range(idx_start, max(idx_start + 1, idx_end - chunk_len), step):
        chunk = mono_audio[i:i + chunk_len]
        if len(chunk) < chunk_len:
            break
        fft = np.abs(np.fft.rfft(chunk * hanning_win))
        freqs = np.fft.rfftfreq(len(chunk), 1.0 / sample_rate)

        bleep_energy = float(np.sum(fft[(freqs >= 985) & (freqs <= 1015)]))
        total_energy = float(np.sum(fft)) + 1e-6
        if bleep_energy / total_energy > 0.45:
            bleep_detected = True
            break

    return {
        "air_voice_ratio": round(peak_air_voice_ratio, 2),
        "air_time": round(peak_air_time, 2),
        "air_energy": peak_air_energy,
        "voice_energy": peak_voice_energy,
        "pitch_f0": pitch_f0_at_peak,
        "mid_mute_drop_db": round(drop_db, 1),
        "bleep_detected": bleep_detected
    }


# ==============================================================================
# 3. ASR ENGINE (whisper.cpp / faster-whisper)
# ==============================================================================

class ASREngine:
    """Обертка над faster-whisper или whisper.cpp для слепого распознавания речи"""
    def __init__(self, model_name: str = "base", device: str = "cpu"):
        self.model_name = model_name
        self.device = device
        self.faster_model = None
        self.align_model = None
        self.whisper_cpp_bin = None
        self.whisper_cpp_model = None
        self._init_engine()

    def _init_engine(self):
        # 1. Проверяем faster-whisper
        try:
            from faster_whisper import WhisperModel
            self.faster_model = WhisperModel(
                self.model_name,
                device=self.device,
                compute_type="int8",
                cpu_threads=2
            )
            return
        except Exception:
            pass

        # 2. Проверяем whisper.cpp бинарник
        candidates = [
            "/opt/whisper-benchmark/whisper.cpp/build/bin/whisper-cli",
            shutil.which("whisper-cli"),
            shutil.which("whisper")
        ]
        for c in candidates:
            if c and os.path.isfile(c) and os.access(c, os.X_OK):
                model_path = f"/opt/whisper-benchmark/whisper.cpp/models/ggml-{self.model_name}.bin"
                if os.path.isfile(model_path):
                    self.whisper_cpp_bin = c
                    self.whisper_cpp_model = model_path
                    return

    def align_segments(self, audio_path: str, language: str = "ru") -> List[Dict[str, Any]]:
        """Быстрое выравнивание текста по времени с использованием легковесной модели tiny (5-8 сек)"""
        if self.faster_model is None:
            return []
        if self.align_model is None:
            try:
                from faster_whisper import WhisperModel
                self.align_model = WhisperModel("tiny", device=self.device, compute_type="int8", cpu_threads=2)
            except Exception:
                self.align_model = self.faster_model
        try:
            segments, _ = self.align_model.transcribe(audio_path, language=language)
            return [{"start": s.start, "end": s.end, "text": s.text.lower()} for s in segments]
        except Exception:
            return []

    def transcribe_file(self, wav_path: str, language: str = "ru") -> str:
        """Слепое распознавание 10-секундного WAV-фрагмента без контекстного prompt"""
        if self.faster_model:
            try:
                segments, _ = self.faster_model.transcribe(
                    wav_path,
                    language=language,
                    beam_size=3
                )
                text = " ".join([s.text for s in segments]).strip()
                return text
            except Exception as e:
                return f"[ASR ERROR: {e}]"

        if self.whisper_cpp_bin and self.whisper_cpp_model:
            try:
                with tempfile.NamedTemporaryFile(suffix=".txt", delete=False) as tf:
                    out_prefix = tf.name[:-4]
                cmd = [
                    self.whisper_cpp_bin,
                    "-m", self.whisper_cpp_model,
                    "-f", wav_path,
                    "-l", language,
                    "-t", "2",
                    "-otxt",
                    "-of", out_prefix
                ]
                subprocess.run(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
                txt_file = out_prefix + ".txt"
                res_text = ""
                if os.path.exists(txt_file):
                    with open(txt_file, "r", encoding="utf-8") as f:
                        res_text = f.read().strip()
                    os.remove(txt_file)
                return res_text
            except Exception as e:
                return f"[whisper.cpp error: {e}]"

        return "[ASR UNAVAILABLE]"


# ==============================================================================
# 4. ДЕТЕКТОР И ПРИНЯТИЕ РЕШЕНИЯ
# ==============================================================================

def normalize_text_for_asr(text: str) -> str:
    """Удаляет пробелы и знаки препинания, заменяя ё на е для нечувствительного к разделителям поиска"""
    return re.sub(r"[^а-яa-z0-9]", "", text.lower().replace("ё", "е"))


# Корневые регулярные выражения для поиска мата и нарко-сленга в сплошном ASR тексте
ASR_ROOT_PATTERNS = {
    "сука": re.compile(r"сук[аеуоыий]?"),
    "сукин": re.compile(r"сук"),
    "сучий": re.compile(r"суч"),
    "бля": re.compile(r"бл[яе][дт]?"),
    "блядь": re.compile(r"бл[яе][дт]?"),
    "блять": re.compile(r"бл[яе][дт]?"),
    "нахуй": re.compile(r"нах[уеяиюёй]"),
    "похуй": re.compile(r"пох[уеяиюёй]"),
    "дохуя": re.compile(r"дох[уеяиюёй]"),
    "хуй": re.compile(r"ху[йеяиюё]"),
    "пизда": re.compile(r"п[иеё]зд"),
    "пиздец": re.compile(r"п[иеё]зд"),
    "пизди": re.compile(r"п[иеё]зд"),
    "пиздит": re.compile(r"п[иеё]зд"),
    "ебать": re.compile(r"[её]б[аеуиоя]"),
    "ебаный": re.compile(r"[её]бан"),
    "ёбаный": re.compile(r"[её]бан"),
    "ебаную": re.compile(r"[её]бан"),
    "съебаться": re.compile(r"с[ъь]?[её]б"),
    "разъебем": re.compile(r"раз[ъь]?[её]б"),
    "заебал": re.compile(r"за[её]б"),
    "охуел": re.compile(r"ох[уеяиюёй]"),
    "охуела": re.compile(r"ох[уеяиюёй]"),
}


def check_word_in_asr_text(target_word: str, asr_text: str) -> bool:
    """
    Проверяет наличие целевого слова в распознанном тексте ASR.
    Устойчив к разделению предлогов Whisper-ом ("с укой", "на хуй", "до хуя"),
    грамматическим флексиям (падежи/склонения) и рассинхрону LRC.
    """
    text_lower = asr_text.lower()
    target_lower = target_word.lower()

    # 1. Прямое совпадение подстроки в исходном тексте
    if target_lower in text_lower:
        return True

    # 2. Поиск в сплошном нормализованном тексте (склейка "с укой" -> "сукой", "на хуй" -> "нахуй")
    norm_text = normalize_text_for_asr(text_lower)
    norm_target = normalize_text_for_asr(target_lower)

    if norm_target in norm_text:
        return True

    # 3. Поиск по корневым морфемам мата
    pat = ASR_ROOT_PATTERNS.get(norm_target)
    if pat and pat.search(norm_text):
        return True

    # 4. Проверка усеченного корня (флексий)
    for flex in [
        "еет", "ает", "ит", "ят", "ут", "ют",
        "ами", "ями", "ах", "ях", "ой", "ей", "ом", "ем", "ам", "ям",
        "а", "я", "у", "ю", "е", "о", "ы", "и"
    ]:
        if norm_target.endswith(flex) and len(norm_target) - len(flex) >= 3:
            stem = norm_target[:-len(flex)]
            if stem in norm_text:
                return True
            break

    # 5. Пословная проверка токенов
    tokens = re.findall(r"[a-zA-Zа-яёА-ЯЁ0-9]+", text_lower)
    for tok in tokens:
        if tok == target_lower:
            return True
        if len(norm_target) >= 4 and tok.startswith(norm_target[:4]):
            return True

    # 6. Фонетическое сопоставление (аканье/иканье в транскрипциях Whisper: косика->косяка, башки->бошки)
    phon_target = norm_target.replace("о", "а").replace("я", "и").replace("е", "и").replace("ё", "и").replace("э", "и")
    phon_text = norm_text.replace("о", "а").replace("я", "и").replace("е", "и").replace("ё", "и").replace("э", "и")
    if len(phon_target) >= 4 and phon_target in phon_text:
        return True

    return False


def find_clean_replacements_in_asr(asr_text: str) -> List[str]:
    """Проверяет наличие эвфемизмов/заменителей цензуры в распознанном тексте"""
    text_lower = asr_text.lower()
    found = []
    for m in CENSORSHIP_SUSPECT_MARKERS:
        if m in text_lower:
            found.append(m)
    return found


def extract_10s_slice(
    audio_path: Path,
    target_time: float,
    total_duration: float
) -> Tuple[str, float, float, float]:
    """
    Вырезает 10-секундное окно [T - 5.0s, T + 5.0s] в 16kHz mono WAV.
    Возвращает (slice_path, start_time, end_time, target_relative_time).
    """
    if total_duration > 0:
        # Защита от рассинхрона LRC: если таймкод за пределами трека
        target_time = max(0.0, min(total_duration, target_time))
        t_end = min(total_duration, target_time + 5.0)
        t_start = max(0.0, target_time - 5.0)
        if t_start >= t_end:
            t_start = max(0.0, t_end - 2.0)
    else:
        t_start = max(0.0, target_time - 5.0)
        t_end = target_time + 5.0

    target_rel = max(0.0, target_time - t_start)

    fd, out_wav = tempfile.mkstemp(suffix=".wav", prefix="censor_slice_")
    os.close(fd)

    cmd = [
        "ffmpeg", "-y",
        "-ss", f"{t_start:.3f}",
        "-to", f"{t_end:.3f}",
        "-i", str(audio_path),
        "-ar", "16000",
        "-ac", "1",
        "-c:a", "pcm_s16le",
        out_wav
    ]
    try:
        subprocess.run(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=True)
    except Exception as e:
        if os.path.exists(out_wav):
            try:
                os.remove(out_wav)
            except Exception:
                pass
        raise RuntimeError(f"ffmpeg failed to extract slice [{t_start:.3f} - {t_end:.3f}] from {audio_path.name}: {e}")

    return out_wav, t_start, t_end, target_rel


def load_wav_mono(wav_path: str) -> Tuple[np.ndarray, int]:
    """Загружает WAV файл в 1D массив numpy float32 [-1.0, 1.0]"""
    import wave
    with wave.open(wav_path, "rb") as wf:
        rate = wf.getframerate()
        n = wf.getnframes()
        data = np.frombuffer(wf.readframes(n), dtype=np.int16)
        mono = data.astype(np.float32) / 32768.0
        return mono, rate


def evaluate_suspect_word(
    word_info: Dict[str, Any],
    audio_path: Path,
    total_duration: float,
    asr_engine: ASREngine,
    slice_cache: Optional[Dict[float, Tuple[str, np.ndarray, int, float, float]]] = None
) -> Dict[str, Any]:
    """
    Выполняет полный цикл проверки одного триггерного слова:
      1. Нарезка 10с окна [-5с, +5с] (с повторным использованием близких окон через slice_cache)
      2. Слепой ASR
      3. Акустический DSP (Air/Voice, F0, Mute, Bleep)
      4. Формирование вердикта
    """
    target_time = word_info["time"]
    target_word = word_info["word"]
    category = word_info["category"]
    canonical_line = word_info.get("line", "")

    # Проверяем, есть ли уже распознанное окно для близкого таймкода (+/- 2.5 сек)
    cached_entry = None
    if slice_cache is not None:
        for c_time, entry in slice_cache.items():
            if abs(target_time - c_time) <= 2.5:
                cached_entry = entry
                break

    if cached_entry is not None:
        asr_heard, mono_audio, sample_rate, w_start, w_end = cached_entry
        rel_target = max(0.0, target_time - w_start)
        slice_path = None
    else:
        try:
            slice_path, w_start, w_end, rel_target = extract_10s_slice(audio_path, target_time, total_duration)
        except Exception as e:
            return {
                "word": target_word,
                "category": category,
                "timestamp": target_time,
                "window_start": target_time,
                "window_end": target_time,
                "canonical_line": canonical_line,
                "asr_heard": "",
                "target_detected": True,
                "dsp": {
                    "air_voice_ratio": 0.0,
                    "air_energy": 0.0,
                    "voice_energy": 0.0,
                    "pitch_f0": True,
                    "mid_mute_drop_db": 0.0,
                    "bleep_detected": False
                },
                "verdict": "ORIGINAL_EXPLICIT",
                "confidence": 50,
                "explanation": f"Пропуск слова (ошибка извлечения среза: {e})"
            }

        try:
            asr_heard = asr_engine.transcribe_file(slice_path, language="ru")
            mono_audio, sample_rate = load_wav_mono(slice_path)
            if slice_cache is not None:
                slice_cache[target_time] = (asr_heard, mono_audio, sample_rate, w_start, w_end)
        finally:
            if slice_path and os.path.exists(slice_path):
                try:
                    os.remove(slice_path)
                except Exception:
                    pass

    # 2. Поиск слова в ASR
    word_detected_in_asr = check_word_in_asr_text(target_word, asr_heard)
    clean_replacements = find_clean_replacements_in_asr(asr_heard)

    # 3. DSP метрики
    dsp = analyze_dsp_features(mono_audio, sample_rate, rel_target, search_window=1.0)

    air_ratio = dsp["air_voice_ratio"]
    pitch_f0 = dsp["pitch_f0"]
    mid_mute_db = dsp["mid_mute_drop_db"]
    bleep = dsp["bleep_detected"]

    # 4. Формирование вердикта
    verdict = "UNKNOWN"
    confidence = 0
    explanation = ""

    if word_detected_in_asr:
        verdict = "ORIGINAL_EXPLICIT"
        confidence = 100
        explanation = "Слово произнесено полностью и разборчиво"
    else:
        # Целевое слово не найдено в аудио
        if clean_replacements:
            verdict = "CENSORED_SUBSTITUTION"
            confidence = 95
            repl_str = ", ".join(clean_replacements)
            explanation = f"Слово заменено на цензурный эвфемизм / заменитель '{repl_str}'"
        elif bleep:
            verdict = "CENSORED_BLEEP"
            confidence = 99
            explanation = "Слово заглушено тональным бипом (1 kHz sine bleep)"
        elif mid_mute_db >= 16.0:
            verdict = "CENSORED_MUTE"
            confidence = 90
            explanation = f"Вокальная дорожка заглушена (вокальный провал {mid_mute_db:.1f} dB)"
        else:
            verdict = "UNVERIFIED_MISS"
            confidence = 50
            explanation = "Слово не распознано в ASR (возможно замаскировано битом или быстрым флоу), явных следов цензуры (бипа, замены, глубокого мьюта) не обнаружено"

    return {
        "word": target_word,
        "category": category,
        "timestamp": target_time,
        "window_start": w_start,
        "window_end": w_end,
        "canonical_line": canonical_line,
        "asr_heard": asr_heard,
        "target_detected": word_detected_in_asr,
        "dsp": dsp,
        "verdict": verdict,
        "confidence": confidence,
        "explanation": explanation
    }


def is_english_track(meta: Dict[str, Any], raw_text: Optional[str]) -> bool:
    """
    Определяет, является ли трек англоязычным/иностранным.
    Англоязычные треки не проверяются на цензуру.
    """
    # 1. По тексту песни (самый надежный источник)
    if raw_text and len(raw_text.strip()) >= 15:
        latin_chars = len(re.findall(r"[a-zA-Z]", raw_text))
        cyrillic_chars = len(re.findall(r"[а-яёА-ЯЁ]", raw_text))
        # Если в тексте латинских букв достаточно много и кириллицы мало (< 15%)
        if latin_chars >= 25 and cyrillic_chars < max(5, int(latin_chars * 0.15)):
            return True
        if cyrillic_chars >= 10:
            return False

    # 2. По метаданным (артист и название)
    artist = (meta.get("artist") or "").strip()
    title = (meta.get("title") or "").strip()
    combined_meta = f"{artist} {title}"
    latin_meta = len(re.findall(r"[a-zA-Z]", combined_meta))
    cyrillic_meta = len(re.findall(r"[а-яёА-ЯЁ]", combined_meta))

    # Если в артисте или названии есть кириллица, это не чистый английский трек
    if cyrillic_meta > 0:
        return False

    # Если метаданные содержат латиницу и нет ни одной буквы кириллицы
    if latin_meta >= 5 and cyrillic_meta == 0:
        if not raw_text or len(re.findall(r"[а-яёА-ЯЁ]", raw_text)) == 0:
            return True

    return False


# ==============================================================================
# 5. АНАЛИЗАТОР ТРЕКА
# ==============================================================================

def analyze_track(
    audio_path: Path,
    asr_engine: ASREngine,
    fast_skip: bool = True
) -> Dict[str, Any]:
    """
    Полный анализ одного трека:
      - Проверка наличия триггеров в тексте / тегах
      - Если англоязычный трек -> CLEAN (0.01с, ENGLISH_SKIPPED)
      - Если триггеров нет -> CLEAN (0.01с)
      - Если есть -> нарезка 10с окон и проверка ASR + DSP
    """
    t0 = time.time()
    try:
        meta = get_audio_info(audio_path)
        duration = meta.get("duration", 0.0)

        # 1. Поиск текста
        source_name, parsed_lines, raw_text = find_lyrics_source(audio_path, meta)

        # Проверка языка: англоязычные треки не проверяются на цензуру
        if is_english_track(meta, raw_text):
            elapsed = time.time() - t0
            return {
                "file": str(audio_path),
                "meta": meta,
                "source": source_name,
                "status": "CLEAN",
                "is_censored": False,
                "triggers_count": 0,
                "findings": [],
                "elapsed_sec": round(elapsed, 3),
                "note": "ENGLISH_SKIPPED"
            }

        # 2. Поиск триггерных слов
        triggers_found = []

        if parsed_lines:
            for idx, line_item in enumerate(parsed_lines):
                line_t = line_item["time"]
                line_txt = line_item["text"]
                found_in_line = find_triggers_in_line(line_txt)
                
                if found_in_line:
                    # Определяем таймкод слова внутри строки
                    next_t = parsed_lines[idx + 1]["time"] if idx + 1 < len(parsed_lines) else line_t + 3.0
                    line_len = max(1, len(line_txt))
                    
                    for tr in found_in_line:
                        c_start = tr["char_start"]
                        if line_t >= 0 and next_t > line_t:
                            interpolated_t = line_t + (next_t - line_t) * (c_start / line_len)
                        elif line_t >= 0:
                            interpolated_t = line_t
                        else:
                            interpolated_t = -1.0

                        triggers_found.append({
                            "word": tr["word"],
                            "category": tr["category"],
                            "time": interpolated_t,
                            "line": line_txt
                        })
        elif raw_text:
            found_in_raw = find_triggers_in_line(raw_text)
            for tr in found_in_raw:
                triggers_found.append({
                    "word": tr["word"],
                    "category": tr["category"],
                    "time": -1.0,
                    "line": tr["word"]
                })

        # Проверка тегов на маркеры подозрения (clean, radio edit)
        title_markers = find_triggers_in_line(meta.get("title", ""))
        for tm in title_markers:
            triggers_found.append({
                "word": tm["word"],
                "category": tm["category"],
                "time": 0.0,
                "line": meta.get("title", "")
            })

        # Устраняем дубликаты
        unique_triggers = []
        seen = set()
        for tr in triggers_found:
            key = (tr["word"].lower(), round(tr["time"], 1))
            if key not in seen:
                seen.add(key)
                unique_triggers.append(tr)

        # Сортировка по приоритету: сначала ст. 6.13 КоАП РФ (наркотики) и маркеры цензуры, затем мат
        unique_triggers.sort(key=lambda tr: (
            0 if tr.get("category") == "DRUG_COAP_6_13" else
            1 if tr.get("category") == "SUSPECT_MARKER" else 2
        ))

        # 3. Быстрый пропуск, если нет триггерных слов
        if not unique_triggers:
            elapsed = time.time() - t0
            return {
                "file": str(audio_path),
                "meta": meta,
                "source": source_name,
                "status": "CLEAN",
                "is_censored": False,
                "triggers_count": 0,
                "findings": [],
                "elapsed_sec": round(elapsed, 3)
            }

        # 4. Если таймкодов нет (например, только .txt), делаем быстрый прогон транскрипции для привязки к таймкодам
        needs_alignment = any(tr["time"] < 0 for tr in unique_triggers)
        if needs_alignment:
            seg_list = asr_engine.align_segments(str(audio_path), language="ru")
            if seg_list:
                for tr in unique_triggers:
                    if tr["time"] < 0:
                        w = tr["word"].lower()
                        best_t = -1.0
                        for s in seg_list:
                            if w in s["text"]:
                                best_t = (s["start"] + s["end"]) / 2.0
                                break
                        if best_t < 0:
                            # Попробуем найти слова из строки
                            line_words = [lw.lower() for lw in re.findall(r"[a-zA-Zа-яёА-ЯЁ0-9]+", tr.get("line", "")) if len(lw) > 3]
                            for s in seg_list:
                                match_count = sum(1 for lw in line_words if lw in s["text"])
                                if match_count >= 2:
                                    best_t = (s["start"] + s["end"]) / 2.0
                                    break
                        tr["time"] = max(0.0, best_t) if best_t >= 0 else duration / 2.0
            else:
                for tr in unique_triggers:
                    if tr["time"] < 0:
                        tr["time"] = duration / 2.0

        # 5. Проверка каждого подозрительного слова с группировкой
        findings = []
        confirmed_explicit_count = 0
        confirmed_censored_count = 0
        MAX_EVAL_TRIGGERS = 25

        # Кэш транскрипций окон: target_time -> (asr_heard, mono_audio, sample_rate, w_start, w_end)
        slice_cache: Dict[float, Tuple[str, np.ndarray, int, float, float]] = {}

        for tr in unique_triggers:
            if len(findings) >= MAX_EVAL_TRIGGERS:
                break
            # ВНИМАНИЕ: Ранний выход по мату убран! Мат не является признаком отсутствия цензуры (КоАП РФ ст. 6.13).
            # Ранний выход по подтвержденной цензуре: если уже найдено 2 дефекта цензуры, трек точно зацензурен
            if confirmed_censored_count >= 2:
                break

            res = evaluate_suspect_word(tr, audio_path, duration, asr_engine, slice_cache=slice_cache)
            findings.append(res)
            if res["verdict"] == "ORIGINAL_EXPLICIT":
                confirmed_explicit_count += 1
            elif res["verdict"].startswith("CENSORED"):
                confirmed_censored_count += 1

        elapsed = time.time() - t0

        defect_types = sorted(list({f["verdict"] for f in findings if f["verdict"].startswith("CENSORED")}))

        # УБРАН ПРИНЦИП EXPLICIT IMMUNITY:
        # Наличие мата в треке не делает его "чистым от цензуры". 
        # В РФ стриминги оставляют мат, но точечно цензурируют нарко-слова по ст. 6.13 КоАП РФ.
        # Если хотя бы один триггер подвергся цензуре (замена, бип, глубокий вокальный мьют) — трек CENSORED!
        if confirmed_censored_count > 0:
            status = "CENSORED"
            has_censorship = True
        elif confirmed_explicit_count > 0:
            status = "ORIGINAL_EXPLICIT"
            has_censorship = False
        else:
            status = "ORIGINAL_EXPLICIT" if findings else "CLEAN"
            has_censorship = False

        return {
            "file": str(audio_path),
            "meta": meta,
            "source": source_name,
            "status": status,
            "is_censored": has_censorship,
            "defect_types": defect_types,
            "triggers_count": len(findings),
            "findings": findings,
            "elapsed_sec": round(elapsed, 2)
        }
    except Exception as e:
        elapsed = time.time() - t0
        return {
            "file": str(audio_path),
            "meta": {},
            "source": "error",
            "status": "ERROR",
            "is_censored": False,
            "defect_types": [],
            "triggers_count": 0,
            "findings": [],
            "elapsed_sec": round(elapsed, 2),
            "error": str(e)
        }


# ==============================================================================
# 6. ВЫВОД И ФОРМАТИРОВАНИЕ ОТЧЕТОВ
# ==============================================================================

def print_single_track_report(report: Dict[str, Any], out=sys.stdout):
    """Выводит подробный человекочитаемый отчет по одному треку (как в Section 4.1 плана)"""
    meta = report.get("meta", {})
    findings = report.get("findings", [])
    duration_str = format_duration(meta.get("duration", 0.0))
    source_str = report.get("source", "Unknown")
    triggers_count = report.get("triggers_count", 0)

    print("========================================================================================", file=out)
    print("🔍 AUDIO CENSORSHIP DETECTOR REPORT", file=out)
    print(f"Track: {report.get('file')}", file=out)
    print(f"Artist: {meta.get('artist')} | Title: {meta.get('title')} | Duration: {duration_str}", file=out)
    print(f"Text Source: {source_str} ({triggers_count} trigger words found)", file=out)
    print("========================================================================================", file=out)

    if not findings:
        print("\n🟢 CLEAN — В треке не обнаружено слов-триггеров мата, нарко-сленга или маркеров цензуры.\n", file=out)
        print("----------------------------------------------------------------------------------------", file=out)
        print("📊 TRACK SUMMARY:", file=out)
        print("  • Status:               🟢 CLEAN (Чистый трек)", file=out)
        print(f"  • Elapsed Time:         {report.get('elapsed_sec', 0.01)} sec", file=out)
        print("========================================================================================", file=out)
        return

    for idx, f in enumerate(findings, 1):
        target_time_str = format_time(f["timestamp"])
        w_start_str = format_time(f["window_start"])
        w_end_str = format_time(f["window_end"])
        
        if f["verdict"] == "ORIGINAL_EXPLICIT":
            verdict_icon = "🟢"
        elif f["verdict"].startswith("CENSORED"):
            verdict_icon = "🔴"
        else:
            verdict_icon = "⚪"
        
        target_icon = "✅ DETECTED IN AUDIO" if f["target_detected"] else "❌ MISSING IN AUDIO"
        
        dsp = f["dsp"]
        air_ratio = dsp["air_voice_ratio"]
        air_e_str = format_energy(dsp["air_energy"])
        voice_e_str = format_energy(dsp["voice_energy"])
        pitch_str = "DETECTED (natural voiced formant)" if dsp["pitch_f0"] else "NONE (vocal cords inactive, turbulent airflow noise)"
        drop_db = dsp["mid_mute_drop_db"]

        print(f"\n[{idx}/{len(findings)}] Word: \"{f['word']}\" (Trigger Category: {f['category']})", file=out)
        print(f"  ├─ Target Timestamp: {target_time_str}", file=out)
        print(f"  ├─ Analyzed Window:  {w_start_str} - {w_end_str} (10.0 sec, [-5s, +5s] context)", file=out)
        print(f"  ├─ Canonical Line:   \"{f['canonical_line']}\"", file=out)
        print(f"  ├─ Blind ASR Heard:  \"{f['asr_heard']}\"", file=out)
        print(f"  │    └─ Target Word: {target_icon}", file=out)
        print("  ├─ Acoustic DSP:", file=out)
        print(f"  │    ├─ Air/Voice Ratio: {air_ratio:.2f}", file=out)
        print(f"  │    ├─ High-Freq Air:   {air_e_str} vs Voiced Energy: {voice_e_str}", file=out)
        print(f"  │    ├─ Pitch F0:        {pitch_str}", file=out)
        print(f"  │    └─ Mid-Mute Drop:   {drop_db:.1f} dB", file=out)
        print(f"  ├─ VERDICT:          {verdict_icon} {f['verdict']} (Confidence: {f['confidence']}%)", file=out)
        print(f"  └─ Explanation:      {f['explanation']}", file=out)

    print("\n----------------------------------------------------------------------------------------", file=out)
    print("📊 TRACK SUMMARY:", file=out)
    
    if report.get("is_censored"):
        defects = ", ".join(report.get("defect_types", []))
        c_count = sum(1 for f in findings if f["verdict"].startswith("CENSORED"))
        o_count = sum(1 for f in findings if f["verdict"] == "ORIGINAL_EXPLICIT")
        print(f"  • Status:               🔴 CENSORED (Обнаружена скрытая цензура)", file=out)
        print(f"  • Defect Types:         [{defects}]", file=out)
        print(f"  • Words Verified:       {len(findings)} ({c_count} censored, {o_count} original)", file=out)
        print(f"  • Elapsed Time:         {report.get('elapsed_sec')} sec ({len(findings)} ASR passes on 10s windows + DSP)", file=out)
        print(f"  • Recommendation:       Требуется замена аудиофайла из альтернативного источника (SoundCloud)", file=out)
    else:
        orig_count = sum(1 for f in findings if f["verdict"] == "ORIGINAL_EXPLICIT")
        print(f"  • Status:               🟢 ORIGINAL_EXPLICIT (Оригинальная версия)", file=out)
        print(f"  • Words Verified:       {orig_count}/{len(findings)} original explicit", file=out)
        print(f"  • Elapsed Time:         {report.get('elapsed_sec')} sec", file=out)
        print(f"  • Recommendation:       Файл оригинальный, замена не требуется", file=out)
        
    print("========================================================================================", file=out)


def print_batch_summary(reports: List[Dict[str, Any]], dir_path: Path):
    """Выводит сводную таблицу пакетного сканирования директории (Section 4.2 плана)"""
    total = len(reports)
    clean_count = sum(1 for r in reports if r.get("status") == "CLEAN")
    original_count = sum(1 for r in reports if r.get("status") == "ORIGINAL_EXPLICIT")
    censored_reports = [r for r in reports if r.get("is_censored")]
    censored_count = len(censored_reports)

    clean_pct = (clean_count / total * 100) if total else 0
    orig_pct = (original_count / total * 100) if total else 0
    cens_pct = (censored_count / total * 100) if total else 0

    print("\n----------------------------------------------------------------------------------------")
    print("📊 BATCH SUMMARY:")
    print(f"  • Total scanned:     {total} tracks")
    print(f"  • Clean by nature:   {clean_count} tracks ({clean_pct:.0f}%)")
    print(f"  • 100% Original:     {original_count} tracks ({orig_pct:.0f}%)")
    print(f"  • Censored detected: {censored_count} tracks ({cens_pct:.0f}%)")

    if censored_reports:
        for idx, cr in enumerate(censored_reports, 1):
            defects = ", ".join(cr.get("defect_types", []))
            print(f"      {idx}. {cr.get('file')} [{defects}]")
            
    print("========================================================================================")


# ==============================================================================
# 7. ТОЧКА ВХОДА (CLI)
# ==============================================================================

def main():
    parser = argparse.ArgumentParser(
        description="Autonomous CLI Audio Censorship Detector (Blind ASR + Acoustic DSP)"
    )
    parser.add_argument("path", help="Path to audio file or directory to scan")
    parser.add_argument("--json", action="store_true", help="Output machine-readable JSON")
    parser.add_argument("--model", default="base", choices=["tiny", "base", "small"], help="ASR model size (default: base)")
    parser.add_argument("--device", default="cpu", choices=["cpu", "cuda"], help="Computation device (default: cpu)")
    parser.add_argument("--resume", action="store_true", help="Resume batch scan by skipping tracks already in report-file")
    parser.add_argument("--report-file", default=None, help="Path to report.txt to inspect for already processed tracks")
    parser.add_argument("--details-file", default=None, help="Path to file for appending full per-word analysis for censored tracks")

    args = parser.parse_args()
    target_path = Path(args.path).resolve()

    if not target_path.exists():
        sys.stderr.write(f"❌ Error: Path does not exist: {target_path}\n")
        sys.exit(1)

    # Инициализация ASR движка
    asr = ASREngine(model_name=args.model, device=args.device)

    # Режим одиночного файла
    if target_path.is_file():
        if target_path.suffix.lower() not in AUDIO_EXTENSIONS:
            sys.stderr.write(f"❌ Error: Unsupported audio extension: {target_path.suffix}\n")
            sys.exit(1)

        report = analyze_track(target_path, asr)
        if args.json:
            print(json.dumps(report, ensure_ascii=False, indent=2))
        else:
            print_single_track_report(report)
        return

    # Режим директории (пакетное сканирование)
    if target_path.is_dir():
        audio_files = []
        for root, _, files in os.walk(target_path):
            for f in sorted(files):
                p = Path(root) / f
                if p.suffix.lower() in AUDIO_EXTENSIONS:
                    audio_files.append(p)

        audio_files.sort()

        if not audio_files:
            sys.stderr.write(f"⚠️ No audio files found in directory: {target_path}\n")
            sys.exit(0)

        # Чтение уже обработанных файлов при --resume
        seen_files = set()
        if args.resume and args.report_file and os.path.exists(args.report_file):
            try:
                with open(args.report_file, "r", encoding="utf-8", errors="ignore") as rf:
                    for line in rf:
                        m = re.match(r"^\[\d+/\d+\]\s+(.*?)\s+(?:\[CLEAN|🟢|🔴|⚠️)", line.strip())
                        if m:
                            seen_files.add(m.group(1).strip())
            except Exception as e:
                sys.stderr.write(f"⚠️ Failed to read resume report file: {e}\n")

        if not args.json:
            print("========================================================================================", flush=True)
            print(f"📁 BATCH SCANNING DIRECTORY: {target_path} ({len(audio_files)} tracks found)", flush=True)
            if seen_files:
                print(f"ℹ️ Resume mode: {len(seen_files)} tracks already processed in report. Skipping them.", flush=True)
            print("========================================================================================", flush=True)

        reports = []
        for idx, af in enumerate(audio_files, 1):
            if af.name in seen_files:
                continue

            rep = analyze_track(af, asr)
            reports.append(rep)

            # Если трек зацензурен и указан details-file, пишем подробный отчет в файл
            if args.details_file and rep.get("is_censored"):
                try:
                    with open(args.details_file, "a", encoding="utf-8") as df:
                        print_single_track_report(rep, out=df)
                        df.flush()
                except Exception:
                    pass

            if not args.json:
                name_str = f"[{idx}/{len(audio_files)}] {af.name}"
                # Дополняем пробелами до фиксированной ширины
                if len(name_str) < 45:
                    name_str = name_str.ljust(45)

                if rep.get("status") == "CLEAN":
                    if rep.get("note") == "ENGLISH_SKIPPED":
                        print(f"{name_str} [CLEAN - English track skipped]       ({rep['elapsed_sec']:.2f}s)", flush=True)
                    else:
                        print(f"{name_str} [CLEAN - No trigger words]       ({rep['elapsed_sec']:.2f}s)", flush=True)
                elif rep.get("status") == "ORIGINAL_EXPLICIT":
                    print(f"{name_str} 🟢 ORIGINAL_EXPLICIT ({rep['triggers_count']}/{rep['triggers_count']} words) ({rep['elapsed_sec']:.2f}s)", flush=True)
                elif rep.get("status") == "ERROR":
                    print(f"{name_str} ⚠️ ERROR [{rep.get('error', 'Unknown')}]   ({rep['elapsed_sec']:.2f}s)", flush=True)
                else:
                    # Censored
                    defects = rep.get("defect_types", ["CENSORED"])
                    # Показываем причину первого дефекта
                    first_cens = next((f for f in rep.get("findings", []) if f["verdict"].startswith("CENSORED")), None)
                    reason = defects[0]
                    if first_cens:
                        w = first_cens["word"]
                        t = format_time(first_cens["timestamp"])
                        coap_tag = " (КоАП 6.13)" if first_cens.get("category") == "DRUG_COAP_6_13" else ""
                        if first_cens["verdict"] in ("CENSORED_MUTE", "CENSORED_MUTED_OR_CUT"):
                            reason = f"Mute on '{w}' @ {t}{coap_tag}"
                        elif first_cens["verdict"] == "CENSORED_BLEEP":
                            reason = f"Bleep on '{w}' @ {t}{coap_tag}"
                        elif first_cens["verdict"] == "CENSORED_SUBSTITUTION":
                            reason = f"Subst on '{w}' @ {t}{coap_tag}"
                        else:
                            reason = f"{first_cens['verdict']} on '{w}' @ {t}{coap_tag}"
                    print(f"{name_str} 🔴 CENSORED [{reason}]   ({rep['elapsed_sec']:.2f}s)", flush=True)

        if args.json:
            print(json.dumps(reports, ensure_ascii=False, indent=2), flush=True)
        else:
            print_batch_summary(reports, target_path)


if __name__ == "__main__":
    main()
