// Package logredact masks secrets and personal data in log text before it
// leaves the process, for example as model evidence (PA-40, approved by the
// user on 2026-10-09). Each match becomes a typed placeholder such as
// [REDACTED:token]; Counts reports how many of each kind were masked.
//
// The patterns are deliberately conservative about identifiers: ordinary hex
// IDs, UUIDs, trace IDs and Unix timestamps are kept, because they are the
// evidence an investigation needs. Card and ID numbers are only masked when
// their check digit is valid.
package logredact

import (
	"regexp"
	"sort"
	"strings"
)

// Kinds of masked data, used in placeholders and counts.
const (
	PrivateKey = "private_key"
	Token      = "token"
	Credential = "credential"
	Secret     = "secret"
	Cookie     = "cookie"
	CloudKey   = "cloud_key"
	Email      = "email"
	IDCard     = "id_card"
	BankCard   = "bank_card"
	Phone      = "phone"
	IP         = "ip"
)

// Counts maps a kind to the number of masked occurrences.
type Counts map[string]int

// Add merges other into c.
func (c Counts) Add(other Counts) {
	for kind, n := range other {
		c[kind] += n
	}
}

// Total is the number of masked occurrences of every kind.
func (c Counts) Total() int {
	total := 0
	for _, n := range c {
		total += n
	}
	return total
}

// Kinds lists the masked kinds in a stable order.
func (c Counts) Kinds() []string {
	kinds := make([]string, 0, len(c))
	for kind := range c {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	return kinds
}

// Options select optional masks. The zero value masks internal IPs too.
type Options struct {
	KeepInternalIPs bool
}

func placeholder(kind string) string { return "[REDACTED:" + kind + "]" }

type rule struct {
	kind    string
	pattern *regexp.Regexp
	// replace returns the replacement for one match, or the match itself to
	// keep it (for example when a check digit is invalid).
	replace func(match []string) (string, bool)
}

var (
	privateKeyBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?s:.*?)(?:-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`)
	urlCredential   = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]{1,20}://)[^\s/:@"']+:[^\s/@"']+@`)
	cookieHeader    = regexp.MustCompile(`(?i)\b(set-cookie|cookie)(\s*["']?\s*[:=]\s*)[^\r\n"]+`)
	authorization   = regexp.MustCompile(`(?i)\b(bearer|basic|token)\s+([A-Za-z0-9._~+/=-]{12,})`)
	jwt             = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	providerToken   = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|glpat-[A-Za-z0-9_-]{20,}|xox[abprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9_-]{20,}|aiops_pat_[A-Za-z0-9_-]{16,}|hvs\.[A-Za-z0-9_-]{20,})`)
	cloudKey        = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b|\bLTAI[0-9A-Za-z]{12,24}\b|\bAIza[0-9A-Za-z_-]{35}\b`)
	// A secret-looking key followed by = or : (also as JSON). The value runs
	// to the next delimiter. Keys inside longer words (token_count, author)
	// do not match.
	secretPair = regexp.MustCompile(`(?i)\b(password|passwd|pwd|secret|client[_-]?secret|api[_-]?key|apikey|access[_-]?key(?:[_-]?id)?|secret[_-]?key|access[_-]?token|refresh[_-]?token|id[_-]?token|auth[_-]?token|session[_-]?(?:id|token)|private[_-]?key|token|authorization|credentials?)\b(\s*["']?\s*[:=]\s*["']?)([^\s"',;&}\]]+)`)
	email      = regexp.MustCompile(`\b[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}(?:\.[A-Za-z0-9-]{1,63})*\.[A-Za-z]{2,24}\b`)
	idCard     = regexp.MustCompile(`\b[1-9]\d{5}(?:19|20)\d{2}(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01])\d{3}[\dXx]\b`)
	// Payment card candidates: 13 to 19 digits, optionally grouped by single
	// spaces or dashes, starting with a card network digit (2-6). Unix
	// timestamps start with 1 and are never candidates.
	bankCard    = regexp.MustCompile(`\b[2-6]\d{3}(?:[ -]?\d){9,15}\b`)
	mobilePhone = regexp.MustCompile(`(?:\+86[- ]?)?\b1[3-9]\d{9}\b`)
	e164Phone   = regexp.MustCompile(`\+[1-9]\d{7,14}\b`)
	privateIPv4 = regexp.MustCompile(`\b(?:10(?:\.\d{1,3}){3}|172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2}|192\.168(?:\.\d{1,3}){2})\b`)
)

func whole(kind string) func([]string) (string, bool) {
	return func([]string) (string, bool) { return placeholder(kind), true }
}

func rules(opts Options) []rule {
	out := []rule{
		{PrivateKey, privateKeyBlock, whole(PrivateKey)},
		{Credential, urlCredential, func(m []string) (string, bool) { return m[1] + placeholder(Credential) + "@", true }},
		{Cookie, cookieHeader, func(m []string) (string, bool) { return m[1] + m[2] + placeholder(Cookie), true }},
		{Token, jwt, whole(Token)},
		{Token, authorization, func(m []string) (string, bool) { return m[1] + " " + placeholder(Token), true }},
		{Token, providerToken, whole(Token)},
		{CloudKey, cloudKey, whole(CloudKey)},
		{Secret, secretPair, func(m []string) (string, bool) {
			// An already masked value, or the scheme word of an
			// "Authorization: Bearer <token>" header whose token is masked.
			if strings.HasPrefix(m[3], "[REDACTED:") || strings.EqualFold(m[3], "bearer") || strings.EqualFold(m[3], "basic") {
				return m[0], false
			}
			return m[1] + m[2] + placeholder(Secret), true
		}},
		{Email, email, whole(Email)},
		{IDCard, idCard, func(m []string) (string, bool) { return placeholder(IDCard), validIDCard(m[0]) }},
		{BankCard, bankCard, func(m []string) (string, bool) { return placeholder(BankCard), validCard(m[0]) }},
		{Phone, mobilePhone, whole(Phone)},
		{Phone, e164Phone, whole(Phone)},
	}
	if !opts.KeepInternalIPs {
		out = append(out, rule{IP, privateIPv4, func(m []string) (string, bool) { return placeholder(IP), validIPv4(m[0]) }})
	}
	return out
}

var defaultRules = rules(Options{})

// Mask returns text with every match replaced by its placeholder.
func Mask(text string) (string, Counts) { return mask(text, defaultRules) }

// MaskWith is Mask with explicit options.
func MaskWith(text string, opts Options) (string, Counts) { return mask(text, rules(opts)) }

func mask(text string, rs []rule) (string, Counts) {
	counts := Counts{}
	for _, r := range rs {
		text = r.pattern.ReplaceAllStringFunc(text, func(match string) string {
			groups := r.pattern.FindStringSubmatch(match)
			if groups == nil {
				groups = []string{match}
			}
			replacement, masked := r.replace(groups)
			if !masked {
				return match
			}
			counts[r.kind]++
			return replacement
		})
	}
	return text, counts
}

// validIDCard checks the ISO 7064 MOD 11-2 check character of a mainland
// China resident ID number.
func validIDCard(id string) bool {
	if len(id) != 18 {
		return false
	}
	weights := []int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	sum := 0
	for i, w := range weights {
		sum += int(id[i]-'0') * w
	}
	return "10X98765432"[sum%11] == strings.ToUpper(id[17:])[0]
}

func validIPv4(address string) bool {
	for _, octet := range strings.Split(address, ".") {
		if len(octet) > 3 || len(octet) > 1 && octet[0] == '0' {
			return false
		}
		n := 0
		for _, r := range octet {
			n = n*10 + int(r-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}

// validCard applies the Luhn check to the digits of a candidate whose prefix
// belongs to a card network. A leading 2 is only the Mastercard 2221-2720
// range, so compact dates such as 20261009... are never cards.
func validCard(candidate string) bool {
	digits := make([]int, 0, 19)
	for _, r := range candidate {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	if digits[0] == 2 {
		prefix := digits[0]*1000 + digits[1]*100 + digits[2]*10 + digits[3]
		if prefix < 2221 || prefix > 2720 {
			return false
		}
	}
	sum := 0
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if (len(digits)-1-i)%2 == 1 {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}
