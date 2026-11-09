package censorcheck

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrCloudWaiting = errors.New("cloud unavailable: resume explicitly after checking access or quota")

type Transcript struct {
	Text      string `json:"text"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	Settings  string `json:"settings"`
	RequestID string `json:"request_id,omitempty"`
}

// Transcriber receives audio only, not expected lyrics or trigger words.
type Transcriber interface {
	Transcribe(context.Context, []byte) (Transcript, error)
	Identity() string
}

type CloudClient struct {
	provider, key, model, endpoint string
	client                         *http.Client
}

func NewCloudClient(provider, key, model string, client *http.Client) (*CloudClient, error) {
	if key == "" || model == "" {
		return nil, fmt.Errorf("cloud key and explicit model are required")
	}
	endpoint := ""
	switch provider {
	case "gemini":
		endpoint = "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(model) + ":generateContent"
	case "deepgram":
		endpoint = "https://api.deepgram.com/v1/listen"
	default:
		return nil, fmt.Errorf("unsupported transcription provider")
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	copyClient := *client
	copyClient.Timeout = 60 * time.Second
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &CloudClient{provider, key, model, endpoint, &copyClient}, nil
}
func (c *CloudClient) Identity() string {
	return c.provider + ":" + c.model + ":verbatim-v1:8000hz:profanity_filter=false"
}
func (c *CloudClient) Transcribe(ctx context.Context, pcm []byte) (Transcript, error) {
	t := Transcript{Provider: c.provider, Model: c.model, Settings: c.Identity()}
	var body []byte
	endpoint := c.endpoint
	contentType := "application/json"
	if c.provider == "gemini" {
		body, _ = json.Marshal(map[string]any{"contents": []any{map[string]any{"parts": []any{
			map[string]any{"text": "Transcribe only words audibly sung or spoken in this audio, in the original language. Preserve all words verbatim. Do not censor, infer missing words, complete lyrics, identify the song, or add commentary. Return empty text if no words are intelligible."},
			map[string]any{"inlineData": map[string]string{"mimeType": "audio/wav", "data": base64.StdEncoding.EncodeToString(wav(pcm))}},
		}}}, "generationConfig": map[string]any{"temperature": 0, "maxOutputTokens": 4096}})
	} else {
		q := url.Values{"model": {c.model}, "language": {"multi"}, "encoding": {"linear16"}, "sample_rate": {"8000"}, "channels": {"1"}, "profanity_filter": {"false"}, "smart_format": {"false"}}
		endpoint += "?" + q.Encode()
		body = pcm
		contentType = "application/octet-stream"
	}
	req, e := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	if e != nil {
		return t, ErrCloudWaiting
	}
	req.Header.Set("Content-Type", contentType)
	if c.provider == "gemini" {
		req.Header.Set("x-goog-api-key", c.key)
	} else {
		req.Header.Set("Authorization", "Token "+c.key)
	}
	resp, e := c.client.Do(req)
	if e != nil {
		return t, fmt.Errorf("%w: transcription request failed", ErrCloudWaiting)
	}
	defer resp.Body.Close()
	// Never persist provider error bodies or request URLs: they may contain credentials.
	if resp.StatusCode != http.StatusOK {
		return t, fmt.Errorf("%w: provider HTTP %d", ErrCloudWaiting, resp.StatusCode)
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if e != nil {
		return t, ErrCloudWaiting
	}
	if c.provider == "gemini" {
		var v struct {
			Candidates []struct {
				FinishReason string `json:"finishReason"`
				Content      struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if json.Unmarshal(data, &v) != nil || len(v.Candidates) == 0 {
			return t, fmt.Errorf("%w: no transcription returned", ErrCloudWaiting)
		}
		if v.Candidates[0].FinishReason != "" && v.Candidates[0].FinishReason != "STOP" {
			return t, fmt.Errorf("%w: incomplete transcription", ErrCloudWaiting)
		}
		for _, p := range v.Candidates[0].Content.Parts {
			t.Text += p.Text
		}
	} else {
		var v struct {
			Metadata struct {
				RequestID string `json:"request_id"`
			} `json:"metadata"`
			Results struct {
				Channels []struct {
					Alternatives []struct {
						Transcript string `json:"transcript"`
					} `json:"alternatives"`
				} `json:"channels"`
			} `json:"results"`
		}
		if json.Unmarshal(data, &v) != nil || len(v.Results.Channels) == 0 || len(v.Results.Channels[0].Alternatives) == 0 {
			return t, fmt.Errorf("%w: no transcription returned", ErrCloudWaiting)
		}
		t.Text = v.Results.Channels[0].Alternatives[0].Transcript
		t.RequestID = v.Metadata.RequestID
	}
	t.Text = strings.TrimSpace(t.Text)
	return t, nil
}
