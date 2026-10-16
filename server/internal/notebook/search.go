package notebook

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Search struct {
	Query   string   `json:"query,omitempty"`
	Mode    string   `json:"mode,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Prefix  string   `json:"prefix,omitempty"`
	Limit   int      `json:"limit,omitempty"`
	Cursor  string   `json:"cursor,omitempty"`
	Deleted bool     `json:"deleted,omitempty"`
}
type Summary struct {
	Key           string   `json:"key"`
	Title         string   `json:"title"`
	Tags          []string `json:"tags"`
	Revision      int64    `json:"revision"`
	UpdatedAt     string   `json:"updated_at"`
	Deleted       bool     `json:"deleted"`
	Snippet       string   `json:"snippet"`
	MatchedFields []string `json:"matched_fields"`
	score         int
}
type SearchResult struct {
	Items      []Summary `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}
type searchCursor struct {
	Generation int64
	Offset     int
	QueryHash  string
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}
func matches(field, term string) bool {
	prefix := strings.HasSuffix(term, "*")
	term = strings.TrimSuffix(term, "*")
	for _, w := range words(field) {
		if w == term || (prefix && strings.HasPrefix(w, term)) {
			return true
		}
	}
	return false
}
func excerpt(body string, terms []string, literal bool) string {
	runes := []rune(body)
	at := 0
	if literal && len(terms) > 0 {
		if i := strings.Index(body, terms[0]); i >= 0 {
			at = utf8.RuneCountInString(body[:i])
		}
	} else {
		// Lowercasing by rune preserves rune offsets, including Cyrillic.
		lower := []rune(strings.ToLower(body))
		best := -1
		for _, term := range terms {
			i := strings.Index(string(lower), strings.TrimSuffix(term, "*"))
			if i >= 0 {
				n := utf8.RuneCountInString(string(lower)[:i])
				if best < 0 || n < best {
					best = n
				}
			}
		}
		if best >= 0 {
			at = best
		}
	}
	start := max(0, at-80)
	end := min(len(runes), start+320)
	out := string(runes[start:end])
	if start > 0 {
		out = "…" + out
	}
	if end < len(runes) {
		out += "…"
	}
	return out
}
func (s *Store) Search(ctx context.Context, in Search) (SearchResult, error) {
	if in.Mode == "" {
		in.Mode = "text"
	}
	if in.Limit == 0 {
		in.Limit = 10
	}
	if in.Mode != "text" && in.Mode != "literal" {
		return SearchResult{}, invalid("mode must be text or literal")
	}
	if in.Limit < 1 || in.Limit > 50 || len(in.Query) > 512 || len(in.Prefix) > 256 || len(in.Tags) > 16 || len(in.Cursor) > 2048 {
		return SearchResult{}, invalid("search limits exceeded")
	}
	terms := []string{}
	if in.Mode == "literal" {
		if in.Query != "" {
			terms = append(terms, in.Query)
		}
	} else {
		for _, raw := range strings.Fields(in.Query) {
			parts := words(raw)
			if len(parts) > 0 && strings.HasSuffix(raw, "*") {
				parts[len(parts)-1] += "*"
			}
			terms = append(terms, parts...)
		}
		if strings.TrimSpace(in.Query) != "" && len(terms) == 0 {
			return SearchResult{}, invalid("text query must contain words")
		}
	}
	cursor := searchCursor{}
	rawCursor := in.Cursor
	in.Cursor = ""
	encoded, _ := json.Marshal(in)
	hash := sha256.Sum256(encoded)
	queryHash := hex.EncodeToString(hash[:])
	if rawCursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(rawCursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Offset < 0 || cursor.QueryHash != queryHash {
			return SearchResult{}, invalid("cursor does not match query")
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SearchResult{}, err
	}
	defer tx.Rollback()
	var generation int64
	if err = tx.QueryRowContext(ctx, "SELECT generation FROM metadata WHERE id=1").Scan(&generation); err != nil {
		return SearchResult{}, err
	}
	if rawCursor != "" && generation != cursor.Generation {
		return SearchResult{}, &Error{Code: "stale_cursor", Message: "notebook changed; restart search"}
	}
	rows, err := tx.QueryContext(ctx, "SELECT data FROM notes ORDER BY key")
	if err != nil {
		return SearchResult{}, err
	}
	defer rows.Close()
	found := []Summary{}
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return SearchResult{}, err
		}
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return SearchResult{}, err
		}
		var r Record
		if err = json.Unmarshal(data, &r); err != nil {
			return SearchResult{}, err
		}
		if r.Deleted != in.Deleted || !strings.HasPrefix(r.Key, in.Prefix) {
			continue
		}
		allTags := true
		for _, tag := range in.Tags {
			has := false
			for _, t := range r.Tags {
				if t == tag {
					has = true
					break
				}
			}
			if !has {
				allTags = false
				break
			}
		}
		if !allTags {
			continue
		}
		fields := []string{r.Key, r.Title, strings.Join(r.Tags, " "), r.Body}
		names := []string{"key", "title", "tags", "body"}
		weights := []int{20, 10, 5, 1}
		matched := make([]bool, 4)
		score := 0
		all := true
		for _, term := range terms {
			anyMatch := false
			for i, f := range fields {
				ok := false
				if in.Mode == "literal" {
					ok = strings.Contains(f, term)
				} else {
					ok = matches(f, term)
				}
				if ok {
					anyMatch = true
					matched[i] = true
					score += weights[i]
				}
			}
			if !anyMatch {
				all = false
				break
			}
		}
		if !all {
			continue
		}
		if in.Query != "" && r.Key == in.Query {
			score += 100
		}
		matchedFields := []string{}
		for i, ok := range matched {
			if ok {
				matchedFields = append(matchedFields, names[i])
			}
		}
		found = append(found, Summary{Key: r.Key, Title: r.Title, Tags: r.Tags, Revision: r.Revision, UpdatedAt: r.UpdatedAt, Deleted: r.Deleted, Snippet: excerpt(r.Body, terms, in.Mode == "literal"), MatchedFields: matchedFields, score: score})
	}
	if err = rows.Err(); err != nil {
		return SearchResult{}, err
	}
	rows.Close()
	if err = tx.Commit(); err != nil {
		return SearchResult{}, err
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score > found[j].score
		}
		return found[i].Key < found[j].Key
	})
	if cursor.Offset > len(found) {
		return SearchResult{}, invalid("cursor offset out of range")
	}
	end := min(len(found), cursor.Offset+in.Limit)
	out := SearchResult{Items: found[cursor.Offset:end]}
	if end < len(found) {
		data, _ := json.Marshal(searchCursor{generation, end, queryHash})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return out, nil
}
