package artifacts

import "testing"

func TestPathProblem(t *testing.T) {
	bad := []string{"", "../x", "/abs", `a\b`, "_oryn/x", "_orynx.html", "a/./b", "con:1", "a.", "a /b", "a//b", "a/", "x\x01y"}
	for _, p := range bad {
		if PathProblem(p) == "" {
			t.Errorf("PathProblem(%q) = \"\", want a problem", p)
		}
	}
	good := []string{"a/b.html", "图片/一.png", "index.html", ".well-known/x"}
	for _, p := range good {
		if got := PathProblem(p); got != "" {
			t.Errorf("PathProblem(%q) = %q, want \"\"", p, got)
		}
	}
}

func TestCaseClash(t *testing.T) {
	if got := CaseClash([]string{"A.html", "b.css", "a.html"}); got != "a.html" {
		t.Errorf("CaseClash = %q, want a.html", got)
	}
	if got := CaseClash([]string{"a.html", "b.html"}); got != "" {
		t.Errorf("CaseClash = %q, want \"\"", got)
	}
}

func TestMimeOf(t *testing.T) {
	for p, want := range map[string]string{"x.HTML": "text/html; charset=utf-8", "a/b.woff2": "font/woff2", "x.bin": "application/octet-stream"} {
		if got := MimeOf(p); got != want {
			t.Errorf("MimeOf(%q) = %q, want %q", p, got, want)
		}
	}
}
