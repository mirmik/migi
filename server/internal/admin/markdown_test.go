package admin

import (
	"strings"
	"testing"
)

func TestRenderAgentMarkdownPreservesMathAndRejectsRawHTML(t *testing.T) {
	rendered, err := renderAgentMarkdown("# Result\n\nInline \\(x^2\\) and $$\\int_0^1 x dx$$.\n\n<script>alert(1)</script>")
	if err != nil {
		t.Fatal(err)
	}
	html := string(rendered)
	for _, expected := range []string{"<h1>Result</h1>", `\(x^2\)`, `$$\int_0^1 x dx$$`} {
		if !strings.Contains(html, expected) {
			t.Fatalf("rendered output missing %q: %s", expected, html)
		}
	}
	if strings.Contains(html, "<script>") || strings.Contains(html, "alert(1)") {
		t.Fatalf("raw HTML reached rendered output: %s", html)
	}
}

func TestRenderAgentMarkdownExpandsLocalFileAndSymbolLinks(t *testing.T) {
	rendered, err := renderAgentMarkdown(
		"See [thing.go](/home/mirmik/project/pkg/thing.go:12), " +
			"[Thing](/home/mirmik/project/pkg/thing.go:42), and [documentation](https://example.com/docs).",
	)
	if err != nil {
		t.Fatal(err)
	}
	output := string(rendered)
	for _, expected := range []string{
		`<code class="local-reference">/home/mirmik/project/pkg/thing.go:12</code>`,
		`<code class="local-reference">Thing — /home/mirmik/project/pkg/thing.go:42</code>`,
		`<a href="https://example.com/docs">documentation</a>`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("rendered output missing %q: %s", expected, output)
		}
	}
	if strings.Contains(output, `href="/home/`) {
		t.Fatalf("local file path remained clickable: %s", output)
	}
}
