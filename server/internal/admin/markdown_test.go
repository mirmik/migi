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
