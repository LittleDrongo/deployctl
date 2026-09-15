package deploy

import (
	"bytes"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/LittleDrongo/deployctl/internal/config"
)

// Complete lines are redacted before output, including values split across writes.
// Oversized lines are discarded whole, so truncation cannot expose secret suffixes.
type redactor struct {
	mu      sync.Mutex
	out     io.Writer
	line    []byte
	discard bool
	replace *strings.Replacer
}

var urlCredentials = regexp.MustCompile(`(\b[a-zA-Z][a-zA-Z0-9+.-]*://)[^\s/@]+@`)
var passwordField = regexp.MustCompile(`(?i)((?:password|passwd|pwd|token|secret)["']?\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;]+)`)
var mysqlCredentials = regexp.MustCompile(`[^\s:/]+:[^\s@]+@((?:tcp|unix)\()`)

func newRedactor(out io.Writer, t config.Target) *redactor {
	var secrets []string
	add := func(value string) {
		for value != "" {
			secrets = append(secrets, value, quote(value), strings.Trim(strconv.Quote(value), "\""))
			_, tail, ok := strings.Cut(value, "=")
			if !ok {
				break
			}
			value = tail
		}
	}
	for _, a := range t.StartArgs {
		add(a.Value)
	}
	for i, arg := range t.RunArgs {
		if _, value, ok := strings.Cut(arg, "="); ok {
			add(value)
		} else if i > 0 && !strings.HasPrefix(arg, "-") {
			add(arg)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	var pairs []string
	for _, value := range secrets {
		pairs = append(pairs, value, "<redacted>")
	}
	return &redactor{out: out, replace: strings.NewReplacer(pairs...)}
}

func (r *redactor) emit() error {
	var text string
	if r.discard {
		text = "remote : oversized diagnostic line omitted"
	} else {
		text = r.replace.Replace(string(r.line))
		text = urlCredentials.ReplaceAllString(text, "${1}<redacted>@")
		text = passwordField.ReplaceAllString(text, "${1}<redacted>")
		text = mysqlCredentials.ReplaceAllString(text, "<redacted>@${1}")
	}
	r.line, r.discard = r.line[:0], false
	_, err := io.WriteString(r.out, text+"\n")
	return err
}

func (r *redactor) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := len(data)
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		part := data
		if end >= 0 {
			part = data[:end]
		}
		if !r.discard {
			if len(r.line)+len(part) > 64*1024 {
				r.line = r.line[:0]
				r.discard = true
			} else {
				r.line = append(r.line, part...)
			}
		}
		if end < 0 {
			break
		}
		if err := r.emit(); err != nil {
			return 0, err
		}
		data = data[end+1:]
	}
	return n, nil
}

func (r *redactor) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.line) > 0 || r.discard {
		return r.emit()
	}
	return nil
}
