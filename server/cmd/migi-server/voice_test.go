package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func voiceTestWAV(seconds int) []byte {
	b := make([]byte, 44+seconds*32000)
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(b)-44))
	binary.LittleEndian.PutUint16(b[44:], 32768)
	binary.LittleEndian.PutUint16(b[46:], 16384)
	return b
}
func TestVoicePCMConversionRejectsWrongFormat(t *testing.T) {
	wav := voiceTestWAV(1)
	pcm, err := voiceFloatPCM(wav)
	if err != nil || len(pcm) != 64000 {
		t.Fatalf("%d %v", len(pcm), err)
	}
	if math.Float32frombits(binary.LittleEndian.Uint32(pcm)) != -1 || math.Float32frombits(binary.LittleEndian.Uint32(pcm[4:])) != 0.5 {
		t.Fatal("PCM scale/endian")
	}
	binary.LittleEndian.PutUint32(wav[24:], 48000)
	if _, err = voiceFloatPCM(wav); err == nil {
		t.Fatal("accepted wrong rate")
	}
	if _, err = voiceFloatPCM([]byte("bad")); err == nil {
		t.Fatal("accepted truncated WAV")
	}
}
func TestVoiceCycleChunksDeduplicatesAndReplaysReply(t *testing.T) {
	sttCalls, modelCalls := 0, 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing auth")
		}
		if r.URL.Path == "/stt" {
			sttCalls++
			b, _ := io.ReadAll(r.Body)
			if len(b) > 20*16000*4 {
				t.Error("STT chunk too long")
			}
			io.WriteString(w, `{"text":"Привет"}`)
			return
		}
		modelCalls++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["model"] != "gemma4-26b-heretic" {
			t.Error("wrong model")
		}
		kwargs, ok := payload["chat_template_kwargs"].(map[string]any)
		if !ok || kwargs["enable_thinking"] != false {
			t.Error("voice replies must disable thinking")
		}
		if _, exists := payload["max_tokens"]; exists {
			t.Error("voice replies must not impose token caps")
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"Здравствуйте!"}}]}`)
	}))
	defer remote.Close()
	broker := newTestBroker(t)
	store, err := newTransferStore(broker, t.TempDir(), 4<<20, 16<<20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(store.root, ".voice-jobs")
	if err = os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	p := &voiceProcessor{files: store, state: state, client: remote.Client(), config: voiceConfig{URL: remote.URL, Token: "test", Model: "gemma4-26b-heretic"}}
	wav := voiceTestWAV(21)
	if _, err = store.share(t.Context(), "g2-request.wav", voiceMIME, "device:phone", bytes.NewReader(wav), int64(len(wav))); err != nil {
		t.Fatal(err)
	}
	p.scan(t.Context())
	// A lost upload response causes a duplicate file with the same request identity.
	if _, err = store.share(t.Context(), "g2-request.wav", voiceMIME, "device:phone", bytes.NewReader(wav), int64(len(wav))); err != nil {
		t.Fatal(err)
	}
	// Ordinary audio is never sent for inference.
	if _, err = store.share(t.Context(), "other.wav", "audio/wav", "device:phone", bytes.NewReader(wav), int64(len(wav))); err != nil {
		t.Fatal(err)
	}
	again := *p
	again.scan(t.Context())
	if sttCalls != 2 || modelCalls != 1 {
		t.Fatalf("stt=%d model=%d", sttCalls, modelCalls)
	}
	replay, _, err := broker.Subscribe(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	replies := 0
	for _, e := range replay {
		if e.Kind == "pager.message" {
			replies++
			if e.Body != "Здравствуйте!" {
				t.Fatal(e.Body)
			}
		}
	}
	if replies != 1 {
		t.Fatalf("replies=%d", replies)
	}
}
