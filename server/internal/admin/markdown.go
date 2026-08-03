package admin

import (
	"bytes"
	"fmt"
	"html"
	"html/template"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

var agentMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

type protectedMath struct {
	placeholder string
	source      string
}

func renderAgentMarkdown(source string) (template.HTML, error) {
	protected, formulas := protectMath(source)
	var rendered bytes.Buffer
	if err := agentMarkdown.Convert([]byte(protected), &rendered); err != nil {
		return "", fmt.Errorf("render agent markdown: %w", err)
	}
	result := rendered.String()
	for _, formula := range formulas {
		result = strings.ReplaceAll(result, formula.placeholder, html.EscapeString(formula.source))
	}
	// Goldmark escapes ordinary text, rejects unsafe link protocols, and omits
	// raw HTML unless explicitly configured with renderer.WithUnsafe.
	return template.HTML(result), nil
}

func protectMath(source string) (string, []protectedMath) {
	prefix := "\ue000MIGIMATH"
	for strings.Contains(source, prefix) {
		prefix += "X"
	}
	var output strings.Builder
	formulas := make([]protectedMath, 0)
	for index := 0; index < len(source); {
		opening, closing := mathDelimiterAt(source, index)
		if opening == "" {
			output.WriteByte(source[index])
			index++
			continue
		}
		end := findMathClosing(source, index+len(opening), closing, opening == "$")
		if end < 0 {
			output.WriteString(opening)
			index += len(opening)
			continue
		}
		placeholder := fmt.Sprintf("%s%d\ue001", prefix, len(formulas))
		formulas = append(formulas, protectedMath{
			placeholder: placeholder,
			source:      source[index : end+len(closing)],
		})
		output.WriteString(placeholder)
		index = end + len(closing)
	}
	return output.String(), formulas
}

func mathDelimiterAt(source string, index int) (string, string) {
	if index > 0 && source[index-1] == '\\' && (index < 2 || source[index-2] != '\\') {
		return "", ""
	}
	for _, delimiter := range []struct{ opening, closing string }{
		{"$$", "$$"}, {"\\[", "\\]"}, {"\\(", "\\)"}, {"$", "$"},
	} {
		if strings.HasPrefix(source[index:], delimiter.opening) {
			return delimiter.opening, delimiter.closing
		}
	}
	return "", ""
}

func findMathClosing(source string, start int, closing string, singleLine bool) int {
	for index := start; index+len(closing) <= len(source); index++ {
		if singleLine && source[index] == '\n' {
			return -1
		}
		if source[index] == '\\' && closing == "$" {
			index++
			continue
		}
		if strings.HasPrefix(source[index:], closing) && index > start {
			return index
		}
	}
	return -1
}
