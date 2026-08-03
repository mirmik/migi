package admin

import (
	"bytes"
	"fmt"
	stdhtml "html"
	"html/template"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var agentMarkdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

var sourceLocationSuffix = regexp.MustCompile(`(?::\d+){1,2}$`)

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
		result = strings.ReplaceAll(result, formula.placeholder, stdhtml.EscapeString(formula.source))
	}
	result, err := expandLocalReferences(result)
	if err != nil {
		return "", err
	}
	// Goldmark escapes ordinary text, rejects unsafe link protocols, and omits
	// raw HTML unless explicitly configured with renderer.WithUnsafe.
	return template.HTML(result), nil
}

func expandLocalReferences(rendered string) (string, error) {
	context := &nethtml.Node{Type: nethtml.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := nethtml.ParseFragment(strings.NewReader(rendered), context)
	if err != nil {
		return "", fmt.Errorf("parse rendered agent markdown: %w", err)
	}
	for _, node := range nodes {
		expandLocalReferenceNode(node)
	}
	var output bytes.Buffer
	for _, node := range nodes {
		if err := nethtml.Render(&output, node); err != nil {
			return "", fmt.Errorf("serialize rendered agent markdown: %w", err)
		}
	}
	return output.String(), nil
}

func expandLocalReferenceNode(node *nethtml.Node) {
	for child := node.FirstChild; child != nil; {
		next := child.NextSibling
		expandLocalReferenceNode(child)
		child = next
	}
	if node.Type != nethtml.ElementNode || node.Data != "a" {
		return
	}
	href := attribute(node, "href")
	localPath, ok := localReferencePath(href)
	if !ok || node.Parent == nil {
		return
	}
	label := strings.TrimSpace(textContent(node))
	visible := localPath
	pathWithoutLocation := sourceLocationSuffix.ReplaceAllString(localPath, "")
	if label != "" && label != localPath && label != filepath.Base(pathWithoutLocation) {
		visible = label + " — " + localPath
	}
	replacement := &nethtml.Node{
		Type: nethtml.ElementNode,
		Data: "code",
		Attr: []nethtml.Attribute{{Key: "class", Val: "local-reference"}},
	}
	replacement.AppendChild(&nethtml.Node{Type: nethtml.TextNode, Data: visible})
	node.Parent.InsertBefore(replacement, node)
	node.Parent.RemoveChild(node)
}

func localReferencePath(href string) (string, bool) {
	decoded, err := url.PathUnescape(href)
	if err != nil {
		decoded = href
	}
	switch {
	case strings.HasPrefix(decoded, "file://"):
		return strings.TrimPrefix(decoded, "file://"), true
	case strings.HasPrefix(decoded, "/") && !strings.HasPrefix(decoded, "//"):
		return decoded, true
	default:
		return "", false
	}
}

func attribute(node *nethtml.Node, key string) string {
	for _, item := range node.Attr {
		if item.Key == key {
			return item.Val
		}
	}
	return ""
}

func textContent(node *nethtml.Node) string {
	if node.Type == nethtml.TextNode {
		return node.Data
	}
	var result strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		result.WriteString(textContent(child))
	}
	return result.String()
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
