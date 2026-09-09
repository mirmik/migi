package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var errVoiceReview = errors.New("voice transcript needs confirmation")

func (p *voiceProcessor) routes(mux *http.ServeMux, wrap func(http.Handler) http.Handler) {
	mux.Handle("GET /v1/chat", wrap(http.HandlerFunc(p.chatHandler)))
	mux.Handle("POST /v1/chat", wrap(http.HandlerFunc(p.chatHandler)))
	mux.Handle("POST /v1/voice", wrap(http.HandlerFunc(p.uploadVoice)))
	mux.Handle("GET /v1/voice/{voiceID}", wrap(http.HandlerFunc(p.reviewVoice)))
	mux.Handle("POST /v1/voice/{voiceID}", wrap(http.HandlerFunc(p.reviewVoice)))
}

func (p *voiceProcessor) uploadVoice(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	name, err := normalizeTransferName(r.Header.Get("X-Migi-Filename"))
	if err != nil {
		http.Error(w, "invalid recording name", 400)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, voiceMaxWAV))
	if err != nil {
		http.Error(w, "recording exceeds 60 seconds", 413)
		return
	}
	if _, err = voiceFloatPCM(raw); err != nil {
		http.Error(w, "expected G2 PCM16 WAV", 400)
		return
	}
	device, _ := r.Context().Value(deviceContextKey{}).(authenticatedDevice)
	source := "device:" + device.ID
	digest := sha256.Sum256(raw)
	p.mu.Lock()
	defer p.mu.Unlock()
	// One upload identity per device/name/content, even after a lost HTTP response.
	files, err := p.files.list(time.Now().UTC())
	if err != nil {
		http.Error(w, "voice storage unavailable", 500)
		return
	}
	for _, f := range files {
		if f.Source == source && f.Name == name && f.SHA256 == hex.EncodeToString(digest[:]) {
			writeJSON(w, 201, map[string]string{"id": f.ID})
			return
		}
	}
	f, err := p.files.store(r.Context(), name, voiceMIME, source, bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		http.Error(w, "voice storage unavailable or full", 503)
		return
	}
	job := voiceJob{AutoSend: r.Header.Get("X-Migi-Voice-Auto-Send") == "1"}
	if err := saveVoiceJob(filepath.Join(p.state, voiceRequestID(f)+".json"), &job); err != nil {
		http.Error(w, "cannot persist voice mode", 503)
		return
	}
	writeJSON(w, 201, map[string]string{"id": f.ID})
}

func (p *voiceProcessor) reviewVoice(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	device, _ := r.Context().Value(deviceContextKey{}).(authenticatedDevice)
	f, err := p.files.get(r.PathValue("voiceID"), time.Now().UTC())
	if err != nil || f.Source != "device:"+device.ID {
		http.Error(w, "voice request not found", 404)
		return
	}
	path := filepath.Join(p.state, voiceRequestID(f)+".json")
	job := voiceJob{}
	raw, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(raw, &job)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "voice state unavailable", 500)
		return
	}
	if r.Method == http.MethodPost {
		defer r.Body.Close()
		var decision struct {
			Decision string `json:"decision"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&decision) != nil || decoder.Decode(&struct{}{}) != io.EOF || (decision.Decision != "confirmed" && decision.Decision != "cancelled" && decision.Decision != "stop") {
			http.Error(w, "expected confirmed, cancelled or stop", 400)
			return
		}
		if decision.Decision == "stop" {
			if !job.AutoSend {
				http.Error(w, "not an automatic voice request", 409)
				return
			}
			if job.EventID == 0 {
				job.StopRequested = true
				job.RetryAt = time.Time{}
				if err := saveVoiceJob(path, &job); err != nil {
					http.Error(w, "cannot persist stop", 500)
					return
				}
			}
		} else if job.Approval != decision.Decision {
			if job.Approval != "awaiting_confirmation" {
				http.Error(w, "request is not awaiting confirmation", 409)
				return
			}
			job.Approval = decision.Decision
			job.Attempts = 0
			job.RetryAt = time.Time{}
			if err := saveVoiceJob(path, &job); err != nil {
				http.Error(w, "cannot persist decision", 500)
				return
			}
		}
	}
	status := job.Approval
	if status == "" {
		status = "transcribing"
	}
	if job.EventID != 0 && job.Transcript == "" {
		status = "failed"
	}
	if job.AutoSend {
		status = "transcribing"
		if job.Transcript != "" {
			status = "dispatching"
		}
		if job.AgentSubmitted {
			status = "running"
		}
		if job.StopRequested {
			status = "stopping"
		}
		if job.EventID != 0 {
			status = "completed"
			if job.Answer == "" && job.Error != "" {
				status = "failed"
			}
			if job.StopRequested {
				status = "cancelled"
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"id": f.ID, "status": status, "transcript": job.Transcript})
}

// Move old recordings, not copies in the public exchange. Keep completed jobs;
// never automatically execute an old recording with no known decision.
func (p *voiceProcessor) migrateLegacy(shared *transferStore) error {
	files, err := shared.list(time.Now().UTC())
	if err != nil {
		return err
	}
	for _, f := range files {
		if f.MIME != voiceMIME {
			continue
		}
		jobPath := filepath.Join(p.state, voiceRequestID(f)+".json")
		if _, err := os.Stat(jobPath); errors.Is(err, os.ErrNotExist) {
			job := voiceJob{Approval: "cancelled"}
			old, err := os.ReadFile(filepath.Join(shared.root, ".voice-jobs", voiceRequestID(f)+".json"))
			if err == nil {
				if err = json.Unmarshal(old, &job); err != nil {
					return err
				}
				if job.AgentSubmitted {
					job.Approval = "confirmed"
				} else {
					job.Approval = "cancelled"
				}
			}
			if err := saveVoiceJob(jobPath, &job); err != nil {
				return err
			}
		}
		// Metadata first: incomplete moves can safely resume on startup; no public
		// routes are accepting requests until migration has finished.
		metadata, _ := json.Marshal(f)
		if err := os.WriteFile(p.files.metadataPath(f.ID), metadata, 0600); err != nil {
			return err
		}
		if err := os.Rename(shared.blobPath(f.ID), p.files.blobPath(f.ID)); err != nil {
			if _, statErr := os.Stat(p.files.blobPath(f.ID)); statErr != nil {
				return fmt.Errorf("move voice %s: %w", f.ID, err)
			}
		}
		if err := os.Remove(shared.metadataPath(f.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	legacy := filepath.Join(shared.root, ".voice-jobs")
	archive := filepath.Join(p.files.root, ".legacy-jobs")
	if _, err := os.Stat(legacy); err == nil {
		if _, err := os.Stat(archive); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(legacy, archive); err != nil {
				return err
			}
		}
	}
	return nil
}
