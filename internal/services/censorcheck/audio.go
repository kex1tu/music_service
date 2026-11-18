package censorcheck

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"time"

	"music-manager/internal/core/safefs"
	"music-manager/internal/core/sandbox"
)

const AlgorithmVersion = "censorship-v1"
const SampleRate = 8000

type Window struct {
	Start float64           `json:"start"`
	End   float64           `json:"end"`
	Spots []QuickListenSpot `json:"spots"`
}

// AuditWindows covers entire lyric lines with context; overlapping windows are
// merged. Splitting bounds memory without imposing a three-spot audit limit.
func AuditWindows(spots []QuickListenSpot, duration float64) []Window {
	ordered := append([]QuickListenSpot(nil), spots...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].TimeSec < ordered[j].TimeSec })
	out := []Window{}
	for _, s := range ordered {
		start, end := math.Max(0, s.TimeSec-4), s.TimeSec+16
		if duration > 0 {
			end = math.Min(end, duration)
		}
		if end <= start {
			continue
		}
		if len(out) > 0 && start <= out[len(out)-1].End && end-out[len(out)-1].Start <= 60 {
			w := &out[len(out)-1]
			w.End = math.Max(w.End, end)
			w.Spots = append(w.Spots, s)
		} else {
			out = append(out, Window{Start: start, End: end, Spots: []QuickListenSpot{s}})
		}
	}
	// Check high-priority themes before other windows, particularly when a
	// bounded cloud quota cannot cover the whole track in a single day.
	priority := func(w Window) int {
		p := 4
		for _, s := range w.Spots {
			switch s.Category {
			case CategoryDrugCoAP, CategorySuicide:
				p = min(p, 1)
			case CategorySuspect:
				p = min(p, 2)
			case CategoryProfane:
				p = min(p, 3)
			}
		}
		return p
	}
	sort.SliceStable(out, func(i, j int) bool {
		if priority(out[i]) != priority(out[j]) {
			return priority(out[i]) < priority(out[j])
		}
		return out[i].Start < out[j].Start
	})
	return out
}

// OpenAudio pins a regular media file inside the library sandbox.
func OpenAudio(sb *sandbox.PathSandbox, rel string) (*os.File, error) {
	p, err := sb.Resolve(rel, false)
	if err != nil {
		return nil, err
	}
	f, err := safefs.Open(p)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("audio is not a readable regular file")
	}
	return f, nil
}

// ffmpeg opens the inherited descriptor independently, so decoding and
// hashing leave the caller's file offset intact.
const audioDescriptor = "/proc/self/fd/3"

func audioCommand(ctx context.Context, f *os.File, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.ExtraFiles = []*os.File{f}
	return cmd
}

// AudioDigest hashes the demuxed audio payload, excluding ID3 and container tags.
func AudioDigest(ctx context.Context, f *os.File) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := audioCommand(cctx, f, "-nostdin", "-v", "error", "-i", audioDescriptor, "-map", "0:a:0", "-c:a", "copy", "-f", "data", "pipe:1")
	h := sha256.New()
	cmd.Stdout = h
	if e := cmd.Run(); e != nil {
		return "", fmt.Errorf("audio digest failed: %w", e)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// DecodeWindow passes a sandbox-pinned media descriptor to ffmpeg. PCM is
// bounded to 60 seconds; no decoded media or transcripts enter MUSIC_DIR.
func DecodeWindow(ctx context.Context, f *os.File, w Window) ([]byte, error) {
	if w.Start < 0 || w.End <= w.Start || w.End-w.Start > 60 {
		return nil, fmt.Errorf("invalid audio window")
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := audioCommand(cctx, f, "-nostdin", "-v", "error", "-ss", strconv.FormatFloat(w.Start, 'f', 3, 64), "-i", audioDescriptor, "-t", strconv.FormatFloat(w.End-w.Start, 'f', 3, 64), "-vn", "-ac", "1", "-ar", strconv.Itoa(SampleRate), "-f", "s16le", "pipe:1")
	b, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("audio decode failed: %w", e)
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("empty decoded audio")
	}
	return b, nil
}

type Signal struct {
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Kind  string  `json:"kind"`
}

// InspectPCM returns hypotheses, never proof of intentional edits. Speech gaps
// and synthesizers deliberately remain reviewable false-positive candidates.
func InspectPCM(pcm []byte, offset float64) []Signal {
	const frame = 800 // 100ms
	out := []Signal{}
	runKind := ""
	runStart := 0
	flush := func(end int) {
		if runKind != "" && end-runStart >= 3 {
			out = append(out, Signal{offset + float64(runStart)/10, offset + float64(end)/10, runKind})
		}
	}
	count := len(pcm) / (frame * 2)
	for k := 0; k < count; k++ {
		x := make([]float64, frame)
		energy := 0.0
		for i := range x {
			x[i] = float64(int16(binary.LittleEndian.Uint16(pcm[(k*frame+i)*2:]))) / 32768
			energy += x[i] * x[i]
		}
		kind := ""
		if energy/float64(frame) < 1e-7 {
			kind = "possible_mute"
		} else {
			for _, freq := range []float64{800, 1000, 1200, 1500, 2000} {
				coef := 2 * math.Cos(2*math.Pi*freq/SampleRate)
				s1, s2 := 0.0, 0.0
				for _, v := range x {
					s := v + coef*s1 - s2
					s2 = s1
					s1 = s
				}
				power := s1*s1 + s2*s2 - coef*s1*s2
				if 2*power/(float64(frame)*energy) > 0.85 {
					kind = "possible_beep"
					break
				}
			}
		}
		if kind != runKind {
			flush(k)
			runKind = kind
			runStart = k
		}
	}
	flush(count)
	return out
}

// ScanSignals streams a full track when lyric guidance is absent. Memory stays
// bounded; the result remains explicitly limited evidence.
func ScanSignals(ctx context.Context, f *os.File) ([]Signal, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := audioCommand(cctx, f, "-nostdin", "-v", "error", "-i", audioDescriptor, "-vn", "-ac", "1", "-ar", "8000", "-f", "s16le", "pipe:1")
	pipe, e := cmd.StdoutPipe()
	if e != nil {
		return nil, e
	}
	if e = cmd.Start(); e != nil {
		return nil, e
	}
	out := []Signal{}
	buf := make([]byte, SampleRate*2*10)
	offset := 0.0
	for {
		n, err := io.ReadFull(pipe, buf)
		if n > 0 {
			for _, s := range InspectPCM(buf[:n], offset) {
				if len(out) < 1000 {
					out = append(out, s)
				}
			}
			offset += float64(n) / (SampleRate * 2)
		}
		if err != nil {
			break
		}
	}
	if e = cmd.Wait(); e != nil {
		return nil, fmt.Errorf("audio scan failed: %w", e)
	}
	if offset == 0 {
		return nil, fmt.Errorf("empty decoded audio")
	}
	return out, nil
}
