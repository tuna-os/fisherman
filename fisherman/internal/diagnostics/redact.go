package diagnostics

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Redacted replaces a secret in a message.
const Redacted = "[redacted]"

// Omitted stands for a value the bundle does not carry.
const Omitted = "[omitted]"

// Other stands for a value that is not one of the values the field
// accepts: the bundle never carries free text in place of an enum.
const Other = "[other]"

// minSecret is the shortest known secret the redactor replaces, as in
// progress.RegisterSecret: a shorter one would match ordinary words.
const minSecret = 4

// secretPatterns match secret shapes in a message, whatever the inputs
// say. They are a second line behind the known secrets, not a proof that
// a message holds no secret (docs/DIAGNOSTICS.md, "Residual limits").
var secretPatterns = []*regexp.Regexp{
	// systemd-cryptenroll recovery key: 8 groups of 8 modhex characters.
	regexp.MustCompile(`(?i)[cbdefghijklnrtuv]{8}(?:-[cbdefghijklnrtuv]{8}){7}`),
	// crypt(3) hashes ("$6$salt$hash", "$y$j9T$...").
	regexp.MustCompile(`\$[0-9a-z]{1,3}\$[^\s:'"]+`),
	// PEM private keys, whole or cut.
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?s:.*?)(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`),
	// Tailscale, GitHub, GitLab and Slack tokens.
	regexp.MustCompile(`tskey-[A-Za-z0-9_-]+`),
	regexp.MustCompile(`(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{8,}|github_pat_[A-Za-z0-9_]{8,}|glpat-[A-Za-z0-9_-]{8,}|xox[abprs]-[A-Za-z0-9-]{8,}`),
	// WireGuard keys and other 32-byte base64 keys.
	regexp.MustCompile(`[A-Za-z0-9+/]{42}[AEIMQUYcgkosw480]=`),
	// Credentials in a URL ("scheme://user:pass@host").
	regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]*:[^/\s@]*@`),
	// HTTP authorization headers and key=value or key: value secrets.
	regexp.MustCompile(`(?i)(?:authorization|proxy-authorization)\s*[:=]\s*\S+(?:\s+\S+)?`),
	regexp.MustCompile(`(?i)\b(?:password|passwd|passphrase|psk|secret|token|creds|credentials|auth[_-]?key|authkey|api[_-]?key|private[_-]?key|wpa[_-]?psk|client[_-]?secret)\b["']?\s*[:=]\s*("[^"]*"|'[^']*'|\S+)`),
	// Registry credentials on a command line (skopeo, podman).
	regexp.MustCompile(`(?i)--(?:src-|dest-)?creds[= ]\S+`),
}

// devicePath matches a /dev path in a message, masked unless identifiers
// are included.
var devicePath = regexp.MustCompile(`/dev/[A-Za-z0-9/_.:-]+`)

// Redactor removes known secrets and secret shapes from messages and masks
// host, user and device identifiers. One Redactor serves a whole bundle,
// so a masked identifier gets the same token in every section.
type Redactor struct {
	secrets            []string
	includeIdentifiers bool
	tokens             map[string]string
	counts             map[string]int
	idents             []string // identifier values, longest first
	redactions         int
}

// NewRedactor returns a Redactor. includeIdentifiers keeps host, user and
// device identifiers in the bundle; secrets are removed either way.
func NewRedactor(includeIdentifiers bool) *Redactor {
	return &Redactor{includeIdentifiers: includeIdentifiers, tokens: map[string]string{}, counts: map[string]int{}}
}

// AddSecret registers a value the bundle must never hold. A short value
// is ignored, as in progress.RegisterSecret.
func (r *Redactor) AddSecret(s string) {
	if len(s) < minSecret {
		return
	}
	for _, have := range r.secrets {
		if have == s {
			return
		}
	}
	r.secrets = append(r.secrets, s)
	// Longest first, so a secret that contains another goes whole.
	sort.SliceStable(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}

// Ident returns the value of an identifier of the given kind ("host",
// "user", "device") as the bundle carries it: the value itself when
// identifiers are included, else a stable token such as "device-1".
// Messages get the same replacement (Message).
func (r *Redactor) Ident(kind, value string) string {
	if value == "" {
		return ""
	}
	if r.includeIdentifiers {
		return value
	}
	if t, ok := r.tokens[value]; ok {
		return t
	}
	r.counts[kind]++
	t := fmt.Sprintf("%s-%d", kind, r.counts[kind])
	r.tokens[value] = t
	if len(value) >= minSecret {
		r.idents = append(r.idents, value)
		sort.SliceStable(r.idents, func(i, j int) bool { return len(r.idents[i]) > len(r.idents[j]) })
	}
	return t
}

// Message returns s as the bundle carries it: known secrets and secret
// shapes replaced with Redacted, identifiers masked, and the result cut to
// at most max bytes. Secrets go before the cut, so a cut never leaves part
// of a secret that the whole value would have matched.
func (r *Redactor) Message(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	// Redact no more than twice what is kept: the cut mark this leaves is
	// handled like fisherman's own (trimSecretPrefix).
	if len(s) > 2*max {
		s = bound(s, 2*max)
	}
	for _, secret := range r.secrets {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, Redacted)
			r.redactions++
		}
		s = trimSecretPrefix(s, secret)
	}
	for _, re := range secretPatterns {
		s = re.ReplaceAllStringFunc(s, func(string) string {
			r.redactions++
			return Redacted
		})
	}
	if !r.includeIdentifiers {
		for _, v := range r.idents {
			s = strings.ReplaceAll(s, v, r.tokens[v])
		}
		s = devicePath.ReplaceAllStringFunc(s, func(p string) string { return r.Ident("device", p) })
	}
	return bound(s, max)
}

// Redactions is how many replacements Message made.
func (r *Redactor) Redactions() int { return r.redactions }

// trimSecretPrefix removes a start of secret that ends s, or ends s before
// a "…" cut mark: fisherman cuts long messages (progress.Failure), and a
// cut can split a secret that Message would otherwise have matched whole.
func trimSecretPrefix(s, secret string) string {
	body, mark := s, ""
	if strings.HasSuffix(body, "…") {
		body, mark = strings.TrimSuffix(body, "…"), "…"
	}
	for k := len(secret) - 1; k >= minSecret; k-- {
		if strings.HasSuffix(body, secret[:k]) {
			return strings.TrimSuffix(body, secret[:k]) + Redacted + mark
		}
	}
	return s
}

// bound cuts s to at most n bytes on a rune boundary, marking the cut.
func bound(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// enum returns v when it is one of allowed (or empty), else Other.
func enum(v string, allowed ...string) string {
	if v == "" {
		return ""
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return Other
}

// token returns v when it matches re and has no secret shape, else Other
// (or "" for "").
func token(v string, re *regexp.Regexp) string {
	if v == "" {
		return ""
	}
	if !re.MatchString(v) || hasSecretShape(v) {
		return Other
	}
	return v
}

func hasSecretShape(v string) bool {
	for _, re := range secretPatterns {
		if re.MatchString(v) {
			return true
		}
	}
	return false
}

var (
	// codeRe is the shape of a stable code (progress.codeRe).
	codeRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	// fieldRe is the shape of a recipe field path ("customMounts[0].fstype").
	fieldRe = regexp.MustCompile(`^[A-Za-z0-9_.\[\]]{1,128}$`)
	// wordRe is a short word: a version, an id, a status.
	wordRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+~:-]{0,63}$`)
	// refRe is an image reference with an optional transport and no
	// credentials: the shape bootc, skopeo and podman take.
	refRe = regexp.MustCompile(`^(?:[a-z][a-z0-9-]*:(?://)?)?[A-Za-z0-9][A-Za-z0-9._/:@+-]{0,511}$`)
	// pathRe is an absolute path of ordinary characters.
	pathRe = regexp.MustCompile(`^/[A-Za-z0-9._/@+-]{0,255}$`)
)

// imageRef returns an image reference as the bundle carries it: the
// reference when it has the plain shape and no credentials, else Omitted.
func imageRef(ref string) string {
	if ref == "" {
		return ""
	}
	// A local path (oci:/dir, containers-storage:[...]) is a host path,
	// not an image identity; an "@" that is not a digest can be
	// credentials.
	local := strings.Contains(strings.ReplaceAll(ref, "://", ""), ":/")
	if !refRe.MatchString(ref) || local || strings.Contains(ref, "@") && !strings.Contains(ref, "@sha256:") || hasSecretShape(ref) {
		return Omitted
	}
	return ref
}
