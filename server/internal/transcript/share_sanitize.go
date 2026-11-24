package transcript

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
	"golang.org/x/text/unicode/norm"
)

const ShareMessageLimit = 1 << 20
const ShareDisplayLimit = 32 << 10

var ErrShareComponentLimit = errors.New("sharing component exceeds complete-content limit")
var privateKeyStart = lazyre.New(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)
var privateKeyEnd = lazyre.New(`(?i)-----END [A-Z0-9 ]*PRIVATE KEY(?: BLOCK)?-----`)
var shareBearer = lazyre.New(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]+`)

// shareSecretName matches any identifier that contains a credential word, so
// OPENAI_API_KEY, DB_PASSWORD, client_secret and X-Auth-Token all count.
const shareSecretName = `[A-Za-z0-9_.-]*(?:api[_-]?key|apikey|secret|passw(?:or)?d|token|credential|private[_-]?key|access[_-]?key|session[_-]?key|authorization|cookie|signature)[A-Za-z0-9_.-]*`

var shareAssignment = lazyre.New(`(?i)(^|[^A-Za-z0-9_.-])(` + shareSecretName + `)(["']?[ \t]*[:=][ \t]*)`)
var shareCredential = lazyre.New(`\b(?:sk-[A-Za-z0-9_-]{16,}|(?:sk|rk|pk)_live_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}|glpat-[A-Za-z0-9_-]{16,}|xox[abprs]-[A-Za-z0-9-]{10,}|AIza[0-9A-Za-z_-]{30,}|(?:AKIA|ASIA)[A-Z0-9]{16}|npm_[A-Za-z0-9]{30,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
var shareURL = lazyre.New(`(?i)\b[a-z][a-z0-9+.-]{1,15}://[^\s<>"']+`)
var shareSensitiveKey = lazyre.New(`(?i)(?:api[_-]?key|apikey|secret|passw(?:or)?d|token|credential|private[_-]?key|access[_-]?key|session[_-]?key|authorization|cookie|signature|^sig$|x-amz-credential)`)
var shareBasicUser = lazyre.New(`(?i)((?:^|\s)(?:-u|--user)[ =]+)("[^"\r\n]*"|'[^'\r\n]*'|\S+)`)
var shareDigits = lazyre.New(`^[0-9]+$`)
var shareCountKey = lazyre.New(`(?i)^(?:max_tokens|max_output_tokens|max_completion_tokens|input_tokens|output_tokens|total_tokens|token_count|token_limit|tokens)$`)
var shareFlagKey = lazyre.New(`(?i)^use_tokens?$`)

// redactAssignments replaces the value after `name:` or `name=` for any
// credential-like name. Quoted values end at their quote; header-style values
// (authorization, password, passwd, secret, cookie) end at the line, since they
// often contain spaces; other values end at whitespace or a delimiter. Only
// explicit count/flag keys retain numeric/boolean values; numeric credentials
// such as password=123456 are still secrets.
func redactAssignments(s string) string {
	var b strings.Builder
	at := 0
	for _, m := range shareAssignment.FindAllStringSubmatchIndex(s, -1) {
		if m[0] < at {
			continue
		}
		name := strings.ToLower(s[m[4]:m[5]])
		valueStart := m[1]
		end := valueStart
		switch {
		case end < len(s) && (s[end] == '"' || s[end] == '\''):
			q := s[end]
			end++
			for end < len(s) && s[end] != q && s[end] != '\n' {
				if s[end] == '\\' && end+1 < len(s) {
					end++
				}
				end++
			}
			if end < len(s) && s[end] == q {
				end++
			}
		case strings.Contains(name, "authorization") || strings.Contains(name, "passw") || strings.Contains(name, "secret") || strings.Contains(name, "cookie"):
			for end < len(s) && s[end] != '\n' && s[end] != '\r' {
				end++
			}
		default:
			for end < len(s) && !strings.ContainsRune(" \t\r\n,;}\"'", rune(s[end])) {
				end++
			}
		}
		value := s[valueStart:end]
		if value == "" || shareCountKey.MatchString(name) && shareDigits.MatchString(value) ||
			shareFlagKey.MatchString(name) && (value == "true" || value == "false") {
			continue
		}
		b.WriteString(s[at:valueStart])
		quoted := value[0] == '"' || value[0] == '\''
		if quoted {
			b.WriteByte(value[0])
		}
		b.WriteString("[REDACTED]")
		if quoted {
			b.WriteByte(value[0])
		}
		at = end
	}
	b.WriteString(s[at:])
	return b.String()
}

// SanitizeShareText sees the entire decoded component before any display clipping.
// An unterminated private-key block removes everything through EOF. This is
// best-effort credential redaction, not a promise to remove sensitive prose.
func SanitizeShareText(s string) (string, error) {
	if len(s) > ShareMessageLimit || !utf8.ValidString(s) {
		return "", ErrShareComponentLimit
	}
	s = norm.NFKC.String(s)
	if len(s) > ShareMessageLimit {
		return "", ErrShareComponentLimit
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	for {
		begin := privateKeyStart.FindStringIndex(s)
		if begin == nil {
			break
		}
		end := privateKeyEnd.FindStringIndex(s[begin[1]:])
		stop := len(s)
		if end != nil {
			stop = begin[1] + end[1]
		}
		s = s[:begin[0]] + "[REDACTED PRIVATE KEY]" + s[stop:]
	}
	s = shareURL.ReplaceAllStringFunc(s, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[REDACTED URL]"
		}
		if u.User != nil {
			u.User = nil
		}
		q := u.Query()
		for k := range q {
			if shareSensitiveKey.MatchString(k) {
				q.Set(k, "[REDACTED]")
			}
		}
		u.RawQuery = q.Encode()
		return u.String()
	})
	s = shareBearer.ReplaceAllString(s, "Bearer [REDACTED]")
	s = shareBasicUser.ReplaceAllString(s, "${1}[REDACTED]")
	s = redactAssignments(s)
	s = shareCredential.ReplaceAllString(s, "[REDACTED]")
	return s, nil
}

// sanitizeShareSegments scans the entire logical text before choosing its final
// segment. If a private-key block spans segments it removes the separators too;
// omit that component rather than exposing an unmarked suffix as a final answer.
func sanitizeShareSegments(texts []string) ([]string, error) {
	separator := "\n" + rand.Text() + "\n"
	clean, err := SanitizeShareText(strings.Join(texts, separator))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(clean, separator)
	if len(parts) != len(texts) {
		return nil, ErrShareComponentLimit
	}
	return parts, nil
}

func ShareDisplayText(s string) string {
	if len(s) <= ShareDisplayLimit {
		return s
	}
	end := ShareDisplayLimit - len("\n[Display truncated]")
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "\n[Display truncated]"
}

// SanitizeShareArguments normalizes decoded strings and redacts sensitive
// object fields while preserving valid serialized JSON.
func SanitizeShareArguments(raw json.RawMessage) (string, error) {
	if len(raw) > ShareMessageLimit {
		return "", ErrShareComponentLimit
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return "", err
	}
	var walk func(any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			return SanitizeShareText(x)
		case []any:
			for i := range x {
				a, err := walk(x[i])
				if err != nil {
					return nil, err
				}
				x[i] = a
			}
			return x, nil
		case map[string]any:
			out := make(map[string]any, len(x))
			for k, a := range x {
				key, err := SanitizeShareText(k)
				if err != nil {
					return nil, err
				}
				if shareSensitiveKey.MatchString(key) {
					keep := a == nil
					switch a.(type) {
					case json.Number:
						keep = shareCountKey.MatchString(key)
					case bool:
						keep = shareFlagKey.MatchString(key)
					}
					if !keep {
						out[key] = "[REDACTED]"
						continue
					}
				}
				a, err = walk(a)
				if err != nil {
					return nil, err
				}
				out[key] = a
			}
			return out, nil
		default:
			return v, nil
		}
	}
	normalized, err := walk(value)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	if len(b) > ShareMessageLimit {
		return "", ErrShareComponentLimit
	}
	return string(b), nil
}
