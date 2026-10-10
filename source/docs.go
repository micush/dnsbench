package main

import (
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	reCode = regexp.MustCompile("`([^`]+)`")
	reBold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reOL   = regexp.MustCompile(`^\d+\.\s+`)
)

// inlineMD renders `code`, **bold** and [links](url) in already-plain text.
func inlineMD(s string) string {
	s = html.EscapeString(s)
	s = reCode.ReplaceAllString(s, "<code>$1</code>")
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	return reLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		u := sub[2]
		if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") &&
			!strings.HasPrefix(u, "#") && !strings.HasPrefix(u, "/") {
			return m
		}
		return `<a href="` + u + `" target="_blank" rel="noopener">` + sub[1] + `</a>`
	})
}

func isTableRow(l string) bool {
	l = strings.TrimSpace(l)
	return strings.HasPrefix(l, "|") && strings.HasSuffix(l, "|") && len(l) > 1
}

func tableCells(l string) []string {
	l = strings.Trim(strings.TrimSpace(l), "|")
	parts := strings.Split(l, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isTableSep(l string) bool {
	for _, c := range tableCells(l) {
		if strings.Trim(c, "-: ") != "" {
			return false
		}
	}
	return true
}

// renderMarkdown converts the small Markdown subset used by README.md to HTML.
// All text is escaped; only the tags produced here reach the page.
func renderMarkdown(md string) string {
	var out strings.Builder
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var para []string
	list := ""
	flushPara := func() {
		if len(para) > 0 {
			out.WriteString("<p>" + inlineMD(strings.Join(para, " ")) + "</p>\n")
			para = nil
		}
	}
	closeList := func() {
		if list != "" {
			out.WriteString("</" + list + ">\n")
			list = ""
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			flushPara()
			closeList()
			out.WriteString(`<pre class="doc-code"><code>`)
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				out.WriteString(html.EscapeString(lines[i]) + "\n")
			}
			out.WriteString("</code></pre>\n")
		case trimmed == "":
			flushPara()
			closeList()
		case strings.HasPrefix(trimmed, "#"):
			flushPara()
			closeList()
			n := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			if n > 4 {
				n = 4
			}
			tag := []string{"", "h3", "h4", "h5", "h6"}[n]
			out.WriteString("<" + tag + ` class="mt-4">` + inlineMD(strings.TrimSpace(trimmed[n:])) + "</" + tag + ">\n")
		case strings.HasPrefix(trimmed, "![") && strings.HasSuffix(trimmed, ")"):
			// Screenshots are for people reading the README on a code host; the in-app
			// page cannot serve them, so they are left out rather than shown as raw text.
			flushPara()
			closeList()
		case strings.HasPrefix(trimmed, "---") || strings.HasPrefix(trimmed, "───"):
			flushPara()
			closeList()
			out.WriteString("<hr>\n")
		case isTableRow(trimmed):
			flushPara()
			closeList()
			out.WriteString(`<table class="doc-table">` + "\n")
			head := true
			for ; i < len(lines) && isTableRow(lines[i]); i++ {
				if head {
					out.WriteString("<thead><tr>")
					for _, c := range tableCells(lines[i]) {
						out.WriteString("<th>" + inlineMD(c) + "</th>")
					}
					out.WriteString("</tr></thead><tbody>\n")
					head = false
					if i+1 < len(lines) && isTableRow(lines[i+1]) && isTableSep(lines[i+1]) {
						i++
					}
					continue
				}
				out.WriteString("<tr>")
				for _, c := range tableCells(lines[i]) {
					out.WriteString("<td>" + inlineMD(c) + "</td>")
				}
				out.WriteString("</tr>\n")
			}
			i--
			out.WriteString("</tbody></table>\n")
		case strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") || reOL.MatchString(trimmed):
			flushPara()
			want, item := "ul", ""
			if reOL.MatchString(trimmed) {
				want, item = "ol", reOL.ReplaceAllString(trimmed, "")
			} else {
				item = strings.TrimSpace(trimmed[2:])
			}
			if list != want {
				closeList()
				out.WriteString("<" + want + ">\n")
				list = want
			}
			out.WriteString("<li>" + inlineMD(item) + "</li>\n")
		default:
			closeList()
			para = append(para, trimmed)
		}
	}
	flushPara()
	closeList()
	return out.String()
}

// readDoc finds README.md / LICENSE.txt: next to the binary (where the
// installer puts them), then the working directory, then its parent.
func readDoc(name string) string {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	dirs = append(dirs, "/opt/dnsbench", ".", "..")
	for _, d := range dirs {
		if b, err := os.ReadFile(filepath.Join(d, name)); err == nil {
			return string(b)
		}
	}
	return name + " not found."
}

// docBody renders a document for the in-app pages.
func docBody(name string) string {
	content := readDoc(name)
	if strings.HasSuffix(name, ".txt") {
		return `<pre class="license-text" style="white-space:pre-wrap;word-break:break-word;font-size:0.949rem;line-height:1.7">` +
			html.EscapeString(content) + "</pre>"
	}
	return renderMarkdown(content)
}
