package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var errAgentPending = errors.New("agent still running")

func validateVoiceAgentURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("agent_url must be a loopback HTTP origin at 127.0.0.1")
	}
	return nil
}

func (p *voiceProcessor) agentRequest(ctx context.Context, method, path string, payload any, out any) (int, error) {
	var raw []byte
	var err error
	if payload != nil {
		raw, err = json.Marshal(payload)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(p.config.AgentURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("agent HTTP %d", resp.StatusCode)
	}
	if out != nil {
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
	}
	return resp.StatusCode, err
}

func (p *voiceProcessor) advanceAgent(ctx context.Context, file transfer, job *voiceJob, path string) error {
	if job.AgentRun == "" {
		job.AgentRun = voiceRequestID(file)
	}
	if job.AgentThread == "" && p.requireApproval {
		// Resolve the active chat and accept the run atomically in the agent host.
		// A lost response repeats the same owner/request ID, even after New chat.
		action := "voice"
		spoken := strings.Trim(strings.ToLower(job.Transcript), " .!?,")
		if spoken == "стоп" || spoken == "остановись" || spoken == "останови выполнение" {
			action = "voice_stop"
		}
		var accepted struct {
			Thread string `json:"thread_id"`
		}
		code, err := p.agentRequest(ctx, "POST", "/migi/chat", map[string]string{
			"owner": file.Source, "action": action, "request_id": job.AgentRun, "text": job.Transcript}, &accepted)
		if code == http.StatusConflict {
			job.Answer = "Агент ещё занят. Этот запрос не поставлен в очередь. Дождитесь ответа или остановите выполнение."
			return saveVoiceJob(path, job)
		}
		if err != nil && code != http.StatusNotFound {
			return errAgentPending
		}
		if code != http.StatusNotFound {
			if accepted.Thread == "" {
				return errAgentPending
			}
			job.AgentThread = accepted.Thread
			if action == "voice_stop" {
				job.Answer = "Остановка запрошена. Дождитесь завершения текущего действия перед новым запросом."
				return saveVoiceJob(path, job)
			}
			job.AgentSubmitted = true
			if err := saveVoiceJob(path, job); err != nil {
				return err
			}
			_, _, _ = p.files.broker.PublishVoiceReply(ctx, job.AgentRun+"-started", "Агент Migi", "Запрос принят. Выполняю.")
			return errAgentPending
		}
	}
	if job.AgentThread == "" {
		job.AgentThread = file.Source
	}
	query := "?threadId=" + url.QueryEscape(job.AgentThread) + "&runId=" + url.QueryEscape(job.AgentRun)
	var status struct {
		Status          string `json:"status"`
		DocumentEventID uint64 `json:"document_event_id"`
		Result          string `json:"result"`
		Error           string `json:"error"`
	}
	code, err := p.agentRequest(ctx, "GET", "/migi/result"+query, nil, &status)
	if code == http.StatusNotFound {
		if job.AgentSubmitted {
			job.Answer = "Агент потерял журнал запроса. Автоматически повторять действие не буду."
			return saveVoiceJob(path, job)
		}
		text := strings.Trim(strings.ToLower(job.Transcript), " .!?,")
		if text == "стоп" || text == "остановись" || text == "останови выполнение" {
			if p.requireApproval {
				_, err := p.agentRequest(ctx, "POST", "/migi/chat", map[string]string{"owner": file.Source, "action": "stop", "request_id": job.AgentRun + "-stop", "thread_id": job.AgentThread}, nil)
				if err != nil {
					return err
				}
			} else if err := p.cancelVoiceAgent(ctx, job.AgentThread, job.AgentRun); err != nil {
				return err
			}
			job.Answer = "Остановка запрошена. Дождитесь завершения текущего действия перед новым запросом."
			return saveVoiceJob(path, job)
		}
		payload := map[string]any{"threadId": job.AgentThread, "runId": job.AgentRun,
			"messages": []map[string]string{{"id": job.AgentRun, "role": "user", "content": job.Transcript}}}
		code, err = p.agentRequest(ctx, "POST", "/migi/submit", payload, nil)
		if code == http.StatusConflict {
			job.Answer = "Агент ещё занят. Этот запрос не поставлен в очередь. Дождитесь ответа или скажите «стоп»."
			return saveVoiceJob(path, job)
		}
		if err != nil {
			return errAgentPending
		} // an acknowledgement may have been lost: look up the SAME id next time
		job.AgentSubmitted = true
		if err := saveVoiceJob(path, job); err != nil {
			return err
		}
		_, _, _ = p.files.broker.PublishVoiceReply(ctx, job.AgentRun+"-started", "Агент Migi", "Запрос принят. Выполняю.")
		return errAgentPending
	}
	if err != nil {
		return errAgentPending
	} // never re-execute accepted work on transport failure
	job.AgentSubmitted = true
	switch status.Status {
	case "completed":
		if status.DocumentEventID != 0 {
			job.EventID = status.DocumentEventID
		}
		job.Answer = strings.TrimSpace(status.Result)
		if job.Answer == "" {
			job.Answer = "Агент завершил запрос без текстового ответа."
		}
	case "cancelled":
		job.Answer = "Выполнение остановлено. Можно отправить новый запрос."
	case "failed":
		job.Answer = "Агент не завершил запрос. Автоматически повторять действия не буду."
	default:
		return errAgentPending
	}
	job.Error = ""
	return saveVoiceJob(path, job)
}

func (p *voiceProcessor) cancelVoiceAgent(ctx context.Context, thread, except string) error {
	entries, err := os.ReadDir(p.state)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(p.state, entry.Name()))
		if err != nil {
			return err
		}
		var job voiceJob
		if json.Unmarshal(raw, &job) != nil || job.AgentThread != thread || job.AgentRun == except || job.EventID != 0 || job.AgentRun == "" {
			continue
		}
		code, err := p.agentRequest(ctx, "POST", "/agent/cancel", map[string]string{"threadId": thread, "runId": job.AgentRun}, nil)
		if err != nil && code != http.StatusNotFound {
			return err
		}
	}
	return nil
}
