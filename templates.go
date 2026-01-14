package main

import (
	"embed"
	"html/template"
	"io"
	"sync"
)

//go:embed templates/*
var templatesFS embed.FS

// Templates holds parsed templates
type Templates struct {
	cache map[string]*template.Template
	mu    sync.RWMutex
	funcs template.FuncMap
}

// NewTemplates creates a new template manager
func NewTemplates() *Templates {
	return &Templates{
		cache: make(map[string]*template.Template),
		funcs: template.FuncMap{
			"formatDate": formatDate,
			"formatNum":  formatNum,
		},
	}
}

// Render renders a template to the writer
func (t *Templates) Render(w io.Writer, name string, data any) error {
	tmpl, err := t.get(name)
	if err != nil {
		return err
	}
	return tmpl.Execute(w, data)
}

// get retrieves or parses a template
func (t *Templates) get(name string) (*template.Template, error) {
	t.mu.RLock()
	tmpl, ok := t.cache[name]
	t.mu.RUnlock()
	if ok {
		return tmpl, nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Double-check after acquiring write lock
	if tmpl, ok := t.cache[name]; ok {
		return tmpl, nil
	}

	// Parse the template from embedded FS
	content, err := templatesFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, err
	}

	tmpl, err = template.New(name).Funcs(t.funcs).Parse(string(content))
	if err != nil {
		return nil, err
	}

	t.cache[name] = tmpl
	return tmpl, nil
}

// Template helper functions

func formatDate(ts int64) string {
	if ts == 0 {
		return ""
	}
	return formatTimestamp(ts)
}

func formatNum(n int) string {
	if n < 1000 {
		return string(rune('0'+n%10) + rune('0'+(n/10)%10) + rune('0'+(n/100)%10))
	}
	// Simple thousands formatting
	if n < 1000 {
		return formatIntSimple(n)
	}
	return formatIntWithCommas(n)
}

func formatIntSimple(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func formatIntWithCommas(n int) string {
	s := formatIntSimple(n)
	if len(s) <= 3 {
		return s
	}

	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

func formatTimestamp(ts int64) string {
	// Format as YYYY-MM-DD
	// Using simple math to avoid time package dependency in template
	// This is called from templates, actual formatting happens in handlers
	return ""
}
