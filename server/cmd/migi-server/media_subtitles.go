package main

import (
	"errors"
	"regexp"
	"time"
)

const maxSubtitleBytes = int64(4 << 20)
const maxVideoSubtitles = 8

var subtitleLanguagePattern = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)

type originSubtitleInput struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Language string `json:"language,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

type playbackSubtitleReference struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Language string `json:"language,omitempty"`
	Default  bool   `json:"default,omitempty"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

func isSubtitleMIME(mime string) bool {
	switch mime {
	case "text/x-ssa", "application/x-subrip", "text/vtt":
		return true
	}
	return false
}

// Only the registering origin may attach its own immutable subtitle objects.
// All byte metadata is resolved by the server, never trusted from the link.
func (s *mediaStore) resolveOriginSubtitles(inputs []originSubtitleInput, mime, agentID string) ([]playbackSubtitleReference, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	if !isVideoMIME(mime) || len(inputs) > maxVideoSubtitles {
		return nil, errors.New("only video may reference up to 8 subtitles")
	}
	refs := make([]playbackSubtitleReference, 0, len(inputs))
	seen := make(map[string]bool)
	defaultSeen := false
	for _, input := range inputs {
		if !mediaIDPattern.MatchString(input.ID) || seen[input.ID] || input.Label == "" || !validMediaText(input.Label, 128) ||
			len(input.Language) > 35 || input.Language != "" && !subtitleLanguagePattern.MatchString(input.Language) || input.Default && defaultSeen {
			return nil, errors.New("invalid subtitle reference, label, language or duplicate default")
		}
		seen[input.ID] = true
		defaultSeen = defaultSeen || input.Default
		object, err := s.getRecord(input.ID, time.Now().UTC())
		if err != nil {
			return nil, errors.New("subtitle media is unavailable")
		}
		if object.RemoteOrigin == nil || object.RemoteOrigin.AgentTokenID != agentID || !isSubtitleMIME(object.MIME) || object.Size > maxSubtitleBytes {
			return nil, errors.New("subtitle must be a supported file from the same origin, up to 4 MiB")
		}
		refs = append(refs, playbackSubtitleReference{ID: object.ID, Label: input.Label, Language: input.Language,
			Default: input.Default, MIME: object.MIME, Size: object.Size, SHA256: object.SHA256})
	}
	return refs, nil
}
