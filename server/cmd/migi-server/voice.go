package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const voiceMIME = "audio/vnd.migi.voice-wav"
const voiceMaxWAV = 44 + 60*16000*2

type voiceConfig struct {
	URL        string `json:"url"`
	ServerName string `json:"server_name"`
	CAFile     string `json:"ca_file"`
	Token      string `json:"token"`
	Model      string `json:"model"`
}
type voiceJob struct {
	Transcript string    `json:"transcript,omitempty"`
	Answer     string    `json:"answer,omitempty"`
	Error      string    `json:"error,omitempty"`
	Attempts   int       `json:"attempts"`
	RetryAt    time.Time `json:"retry_at"`
	EventID    uint64    `json:"event_id,omitempty"`
}
type voiceProcessor struct {
	config voiceConfig
	client *http.Client
	files  *transferStore
	state  string
}

func newVoiceProcessor(configPath string, files *transferStore) (*voiceProcessor, error) {
	info, err := os.Stat(configPath)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("voice config must have mode0600")
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var config voiceConfig
	if err = json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	u, err := url.Parse(config.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("voice URL must be HTTPS")
	}
	if config.Model == "" || config.Token == "" || config.CAFile == "" {
		return nil, errors.New("voice model, token and CA required")
	}
	pem, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("invalid voice CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, ServerName: config.ServerName}, ResponseHeaderTimeout: 90 * time.Second}
	processor := &voiceProcessor{config: config, files: files, state: filepath.Join(files.root, ".voice-jobs"), client: &http.Client{Transport: transport, Timeout: 100 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return processor, os.MkdirAll(processor.state, 0700)
}

func (p *voiceProcessor) run(ctx context.Context) {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.scan(ctx)
		}
	}
}
func voiceRequestID(file transfer) string {
	sum := sha256.Sum256([]byte(file.Source + "\x00" + file.Name + "\x00" + file.SHA256))
	return hex.EncodeToString(sum[:])
}
func (p *voiceProcessor) scan(ctx context.Context) {
	files, err := p.files.list(time.Now().UTC())
	if err != nil {
		slog.Error("voice list failed", "error", err)
		return
	}
	// Process oldest first; only authenticated device uploads bearing the explicit voice MIME.
	for i := len(files) - 1; i >= 0; i-- {
		file := files[i]
		if file.MIME != voiceMIME || !strings.HasPrefix(file.Source, "device:") {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		id := voiceRequestID(file)
		path := filepath.Join(p.state, id+".json")
		var job voiceJob
		raw, err := os.ReadFile(path)
		if err == nil {
			if json.Unmarshal(raw, &job) != nil {
				slog.Error("invalid voice job", "request", id)
				continue
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if job.EventID != 0 || time.Now().Before(job.RetryAt) {
			continue
		}
		p.process(ctx, file, id, path, &job)
	}
}
func saveVoiceJob(path string, job *voiceJob) error {
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path+".new", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(path+".new", path)
}
func (p *voiceProcessor) process(ctx context.Context, file transfer, id, path string, job *voiceJob) {
	if job.Answer == "" && job.Attempts < 3 {
		job.Attempts++
		job.RetryAt = time.Now().Add(15 * time.Second)
		if err := saveVoiceJob(path, job); err != nil {
			return
		}
		err := p.infer(ctx, file, job, path)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			job.Error = err.Error()
			slog.Warn("voice inference failed", "request", id, "attempt", job.Attempts, "error", err)
			if job.Attempts < 3 {
				_ = saveVoiceJob(path, job)
				return
			}
		}
	}
	title := "Ответ · " + p.config.Model
	body := job.Answer
	if body == "" {
		title = "Голосовой запрос не выполнен"
		body = "Не удалось получить ответ. Запись сохранена на сервере. Повторите запрос позже."
	}
	event, _, err := p.files.broker.PublishVoiceReply(ctx, id, title, body)
	if err != nil {
		slog.Error("voice reply publication failed", "request", id, "error", err)
		return
	}
	job.EventID = event.ID
	job.RetryAt = time.Time{}
	if err = saveVoiceJob(path, job); err != nil {
		slog.Error("voice reply state failed", "request", id, "error", err)
	}
	slog.Info("voice reply delivered", "request", id, "file", file.ID, "event_id", event.ID, "model", p.config.Model)
}
func (p *voiceProcessor) infer(ctx context.Context, file transfer, job *voiceJob, path string) error {
	if job.Transcript == "" {
		if file.Size > voiceMaxWAV {
			return errors.New("voice recording exceeds60 seconds")
		}
		_, reader, err := p.files.OpenSharedFile(ctx, file.ID)
		if err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(reader, voiceMaxWAV+1))
		reader.Close()
		if err != nil {
			return err
		}
		pcm, err := voiceFloatPCM(raw)
		if err != nil {
			return err
		}
		var parts []string
		const chunk = 20 * 16000 * 4
		for offset := 0; offset < len(pcm); offset += chunk {
			end := min(offset+chunk, len(pcm))
			var response struct {
				Text string `json:"text"`
			}
			if err = p.post(ctx, "/stt", "application/octet-stream", pcm[offset:end], &response); err != nil {
				return fmt.Errorf("STT: %w", err)
			}
			if text := strings.TrimSpace(response.Text); text != "" {
				parts = append(parts, text)
			}
		}
		job.Transcript = strings.Join(parts, " ")
		if job.Transcript == "" {
			return errors.New("speech was not recognized")
		}
		if err = saveVoiceJob(path, job); err != nil {
			return err
		}
	}
	payload := map[string]any{"model": p.config.Model, "stream": false, "temperature": 0.3,
		"chat_template_kwargs": map[string]bool{"enable_thinking": false},
		"messages":             []map[string]string{{"role": "system", "content": "Отвечай по-русски кратко и по существу. Ответ читают на маленьком экране очков: обычно достаточно 1–3 предложений. Используй простой текст без Markdown."}, {"role": "user", "content": job.Transcript}}}
	raw, _ := json.Marshal(payload)
	var response struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := p.post(ctx, "/v1/chat/completions", "application/json", raw, &response); err != nil {
		return fmt.Errorf("model: %w", err)
	}
	if len(response.Choices) == 0 {
		return errors.New("model returned no choices")
	}
	answer := strings.TrimSpace(response.Choices[0].Message.Content)
	if answer == "" {
		return fmt.Errorf("model returned no text (finish_reason=%s)", response.Choices[0].FinishReason)
	}
	job.Answer = answer
	job.Error = ""
	return saveVoiceJob(path, job)
}
func (p *voiceProcessor) post(ctx context.Context, path, contentType string, raw []byte, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.config.URL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Authorization", "Bearer "+p.config.Token)
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP%d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil {
		return err
	}
	if len(body) > 1<<20 {
		return errors.New("response too large")
	}
	return json.Unmarshal(body, out)
}
func voiceFloatPCM(wav []byte) ([]byte, error) {
	if len(wav) < 44 || len(wav) > voiceMaxWAV || string(wav[:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || string(wav[36:40]) != "data" {
		return nil, errors.New("expected Migi PCM WAV")
	}
	if binary.LittleEndian.Uint32(wav[4:8]) != uint32(len(wav)-8) || binary.LittleEndian.Uint32(wav[16:20]) != 16 || binary.LittleEndian.Uint16(wav[20:22]) != 1 || binary.LittleEndian.Uint16(wav[22:24]) != 1 || binary.LittleEndian.Uint32(wav[24:28]) != 16000 || binary.LittleEndian.Uint16(wav[34:36]) != 16 || binary.LittleEndian.Uint32(wav[40:44]) != uint32(len(wav)-44) || (len(wav)-44)%2 != 0 {
		return nil, errors.New("expected16kHz mono PCM16 WAV")
	}
	samples := wav[44:]
	if len(samples) < 9600 {
		return nil, errors.New("voice recording too short")
	}
	pcm := make([]byte, len(samples)*2)
	for i := 0; i < len(samples)/2; i++ {
		v := float32(int16(binary.LittleEndian.Uint16(samples[i*2:]))) / 32768
		binary.LittleEndian.PutUint32(pcm[i*4:], math.Float32bits(v))
	}
	return pcm, nil
}
