package service

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/entire-vc/evc-mesh/internal/domain"
)

// Hard or soft: the class a "❓ Blocking @person" ask arms its human gate with
//
//
// Before this, the class came only from a "[soft]" tag on the marker line, so almost
// every ask armed hard and its recommended default never applied: the
// default-on-timeout sweep skips hard gates by design. The rule is
// now the one the Runner already uses to send a soft `WAIT decision:@person` to the
// project lead (evc-mesh-runner src/mesh_runner/asks.py): a question that matches no
// hard rule is not a hard gate on the person. It is decided here, on the one arming
// path every ask goes through (ask_human, a hand-written marker, the marker the Runner
// posts for a `WAIT decision`, the web UI), so no client has to pass a class.
//
// Fail-closed throughout: a hard rule wins over a "[soft]" tag, a "[hard]" tag wins
// over everything, and a question without a default stated by its own author stays
// hard (the system fallback default is a placeholder, not an answer anyone gave).

// hardAskPatterns mirrors asks.py DEFAULT_HARD_PATTERNS, plus the outward-contact
// stems (a letter or call in someone's name) found on live gates on 2026-11-11.
// Fragments are matched case-insensitively at the start of a word; a trailing \b
// means "whole word". Keep the two lists in step: a pattern added on one side only
// makes the Runner and the server disagree about the same question.
var hardAskPatterns = []string{
	// money and payments
	`pay\b`, `pays\b`, `paid\b`, `payment`, `billing`, `invoice`, `price\b`, `prices\b`, `pricing`,
	`refund`, `charge\b`, `charged\b`, `subscription`, `money`, `purchase`, `budget`,
	`оплат`, `платн`, `платеж`, `платёж`, `платит`, `счёт`, `счет\b`, `счета\b`, `деньг`, `денежн`, `цена\b`,
	`цены\b`, `цену\b`, `цен[аыу]\b`, `стоимост`, `тариф`, `возврат`, `бюджет`, `купит`, `покупк`,
	// a top-up, a balance or an amount of money is money too (a live gate asked to "top up
	// the shared balance by $20"); the space before a currency may be a no-break one
	`top[- ]?up`, `credits?\b`, `deposit`, `balance`, `пополн`, `баланс`, `перев(?:од|ест|ед|ож)[а-яё]*\s+(?:денег|деньги|средств[а-яё]*)`,
	`[$€₽£][\s ]?\d`, `\d[\d.,]*[\s ]?(?:usd|eur|руб|₽|\$)`,
	// a currency or an amount spelled as a word ("20 долларов", "50 евро", "5к"), and a
	// "drop/throw" verb with an amount a few words on ("скинуть 20 на OpenRouter"); "евро"
	// only as a whole word. Go's \s is ASCII-only, hence the explicit no-break space
	`доллар`, `евро\b`, `dollar`, `euros?\b`, `бакс`, `\d[\d.,]*[\s ]?(?:k|к|тыс)\b`,
	`(?:за|с)кин[уь][а-яё]*(?:[\s ]+[^\s ]+){0,3}?[\s ]+\d`,
	// secrets, keys, credentials, access
	`secret`, `key\b`, `keys\b`, `token`, `password`, `passphrase`, `credential`, `access\b`,
	`permission`, `api[ _-]?key`, `доступ`, `ключ`, `токен`, `парол`, `секрет`, `учётн`, `учетн`,
	`разрешени`, `role\b`, `рол[ьи]\b`,
	// irreversible or destructive actions on data or production
	`delete`, `drop\b`, `wipe`, `destroy`, `purge`, `truncate`, `irreversib`, `force[- ]push`,
	`prod(?:uction)?[ -]?(?:data|db|database)`, `production\b`, `\bprod\b`,
	`удал`, `снести`, `снесу`, `сносим`, `стереть`, `необратим`, `боев`, `прод\b`, `продакшен`,
	// external publication or contact on behalf of people or the company (a letter, an
	// offer, a call: Russian and English stems alike)
	`publish`, `announce`, `post\b`, `tweet`, `press[- ]release`, `newsletter`, `mass[- ]mail`,
	`опубликов`, `публикац`, `пост\b`, `анонс`, `рассылк`, `пресс-?релиз`,
	`e-?mail`, `письм`, `whatsapp`, `вацап`, `ватсап`, `звон`, `позвон`, `phone\b`,
	`call\b`, `calls\b`, `calling\b`, `letter`, `offer\b`, `offers\b`, `proposal`, `outreach`,
	`reach(?:ing)? out\b`,
	`write to\b`, `writing to\b`, `wrote to\b`, `reply to\b`, `contact\b`, `contacting\b`,
	`message (?:him|her|them)\b`, `text (?:him|her|them)\b`, `dm\b`,
	`написать (?:ему|ей|им|клиент|лид|заказчик|партн|поставщик)`, `напишу (?:ему|ей|им|клиент|лид|заказчик|партн|поставщик)`, `ответить (?:ему|ей|им|клиент|лид|заказчик|партн|поставщик)`, `отвечу (?:ему|ей|им|клиент|лид|заказчик|партн|поставщик)`, `связат`,
	`от (?:вашего|твоего|моего|его|её|ее|нашего) имени`, `on (?:your|his|her|my|our) behalf`,
	`in (?:your|his|her|my|our) name`, `кп\b`, `коммерческ`,
	// something only the person can do with their own hands or logins
	`log[- ]?in\b`, `sign[- ]?in\b`, `click\b`, `press\b`, `captcha`, `2fa\b`, `screenshot`, `iphone`,
	`botfather`, `physical`, `connect\b`,
	`нажм`, `войд`, `войти`, `вход\b`, `авториз`, `откро[йю]`, `скриншот`, `капч`,
	`физическ`, `руками`, `залогин`, `перетащ`, `макбук`, `drag\b`, `macbook`,
	// legal
	`legal`, `contract`, `licen[sc]`, `gdpr`, `lawsuit`, `trademark`, `terms of service`, `compliance`,
	`договор`, `юрид`, `лиценз`, `оферт`, `судебн`,
}

// hardAskLabels mirrors asks.py DEFAULT_HARD_LABELS.
var hardAskLabels = []string{"money", "security", "legal", "prod-data", "publish"}

// wordEnd stands in for a trailing \b: Go's \b is ASCII-only, so `счет\b` would never
// match Cyrillic text and a hard question would silently read as soft. It is the only
// capturing group of the alternation (every fragment groups with (?:…)), so matchHardAsk
// can drop exactly the character it consumed and nothing a fragment matched itself
// (the "$" of "20$").
const wordEnd = `($|[^\p{L}\p{N}_])`

// hardAskRegex is the alternation of every fragment, anchored at the position it is
// tried from. Word-start anchoring (Python's (?<!\w), which RE2 lacks) is done by
// matchHardAsk trying only at word starts.
var hardAskRegex = compileHardAskRegex(hardAskPatterns)

func compileHardAskRegex(patterns []string) *regexp.Regexp {
	parts := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimPrefix(p, `\b`)
		if strings.HasSuffix(p, `\b`) {
			p = strings.TrimSuffix(p, `\b`) + wordEnd
		}
		parts = append(parts, "(?:"+p+")")
	}
	return regexp.MustCompile(`(?i)^(?:` + strings.Join(parts, "|") + `)`)
}

func isAskWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// matchHardAsk returns the first hard fragment found at a word start, lowercased
// (the character wordEnd consumed trimmed), or "".
func matchHardAsk(text string) string {
	prev := rune(-1)
	for i, r := range text {
		if prev == -1 || !isAskWordRune(prev) {
			if loc := hardAskRegex.FindStringSubmatchIndex(text[i:]); len(loc) >= 2 && loc[1] > 0 {
				end := loc[1]
				for g := 2; g+1 < len(loc); g += 2 {
					if loc[g] >= 0 && loc[g+1] == loc[1] && loc[g+1] > loc[g] {
						end = loc[g]
						break
					}
				}
				return strings.ToLower(text[i : i+end])
			}
		}
		prev = r
	}
	return ""
}

// askVerdict is the hard/soft verdict of one question, with the rule that decided it.
type askVerdict struct {
	Hard bool
	Rule string
}

// classifyAsk is asks.classify: hard unless nothing in the question or the labels is
// hard and the question text is there to read.
func classifyAsk(question string, labels []string) askVerdict {
	names := make(map[string]bool, len(labels))
	for _, l := range labels {
		names[strings.ToLower(strings.TrimSpace(l))] = true
	}
	for _, l := range hardAskLabels {
		if names[l] {
			return askVerdict{Hard: true, Rule: "label:" + l}
		}
	}
	text := strings.Join(strings.Fields(question), " ")
	if text == "" {
		return askVerdict{Hard: true, Rule: "unsure: no question text"}
	}
	if m := matchHardAsk(text); m != "" {
		return askVerdict{Hard: true, Rule: "keyword:" + m}
	}
	return askVerdict{Hard: false, Rule: "no hard rule matched"}
}

// blockingMarkerHardTagRegex is the explicit opt-in to a hard gate for a question the
// keyword list does not recognise as hard.
var blockingMarkerHardTagRegex = regexp.MustCompile(`(?i)\[\s*hard\s*\]`)

// askTextForSlug is the question the marker naming slug asks: the rest of its own line
// and everything after it in the same comment (asks.blocking_question), quoted spans
// stripped. "" when no marker names slug.
func askTextForSlug(body, slug string) string {
	lines := strings.Split(stripQuotedSpans(body), "\n")
	for i, line := range lines {
		m := blockingMarkerRegex.FindStringSubmatch(line)
		if len(m) < 2 || strings.ToLower(m[1]) != slug {
			continue
		}
		loc := blockingMarkerRegex.FindStringIndex(line)
		rest := append([]string{line[loc[1]:]}, lines[i+1:]...)
		return strings.TrimSpace(strings.Join(rest, "\n"))
	}
	return ""
}

// markerGateClass is the class the marker naming slug arms its gate with, and the
// rule that decided it (for the log and the reclassification notice).
func markerGateClass(body, slug string, labels []string) (class domain.HumanGateClass, rule string) {
	stripped := stripQuotedSpans(body)
	var markerLine string
	for _, line := range strings.Split(stripped, "\n") {
		m := blockingMarkerRegex.FindStringSubmatch(line)
		if len(m) >= 2 && strings.ToLower(m[1]) == slug {
			markerLine = line
			break
		}
	}
	if blockingMarkerHardTagRegex.MatchString(markerLine) {
		return domain.HumanGateClassHard, "tag:hard"
	}
	if !askWordRegex.MatchString(askedText(askTextForSlug(body, slug))) {
		return domain.HumanGateClassHard, "unsure: no question text"
	}
	v := classifyAsk(blockingMarkerClassRegex.ReplaceAllString(askTextForSlug(body, slug), " "), labels)
	if v.Hard {
		return domain.HumanGateClassHard, v.Rule
	}
	if blockingMarkerClassForSlug(body, slug) == domain.HumanGateClassSoft {
		return domain.HumanGateClassSoft, "tag:soft"
	}
	if d := markerDefault(body, slug); d == "" || d == domain.DefaultMarkerRecommendedDefault {
		return domain.HumanGateClassHard, "no default stated by the asker"
	}
	return domain.HumanGateClassSoft, v.Rule
}

// inlineDefaultRegex is a default stated inside the ask's own sentence, e.g.
// "❓ Blocking @alex: Approve the plan? Default: yes, as is". Only ":" or "—" count as
// the separator here: a "default - …" in prose is too easily something else.
var inlineDefaultRegex = regexp.MustCompile(
	`(?i)(?:^|[^\p{L}\p{N}_])(?:recommended[ _-]?default|default|по\s+умолчанию|дефолт)\**\s*[:—]\**\s*(.+)$`)

// askWordRegex is a letter or a digit: an ask without one asks nothing (":" alone).
var askWordRegex = regexp.MustCompile(`[\p{L}\p{N}]`)

// askMetaLineRegex is a deadline or supersedes line of an ask: metadata, not its question.
var askMetaLineRegex = regexp.MustCompile(`(?i)^[\s>*\-•]*\**\s*(?:deadline|supersedes)\**\s*:`)

// askedText is the question of a marker's text (askTextForSlug) without its default,
// deadline and supersedes lines and without class tags; "" when the marker asks nothing,
// so a marker carrying only a default fails closed instead of reading the default as its
// question (asks._asked_text).
func askedText(text string) string {
	lines := strings.Split(text, "\n")
	first := lines[0]
	if loc := inlineDefaultRegex.FindStringIndex(first); loc != nil {
		first = first[:loc[0]]
	}
	kept := []string{first}
	for _, l := range lines[1:] {
		if recommendedDefaultRegex.MatchString(l) || askMetaLineRegex.MatchString(l) {
			continue
		}
		kept = append(kept, l)
	}
	joined := blockingMarkerClassRegex.ReplaceAllString(
		blockingMarkerHardTagRegex.ReplaceAllString(strings.Join(kept, "\n"), " "), " ")
	return strings.Join(strings.Fields(joined), " ")
}

// markerDefault is the default the ask naming slug states: on the marker's own line,
// else on a "Default:" line after it in the same comment; "" when the ask states none.
// A "Default:" line above the marker is context, not the ask's: a comment often carries
// other defaults there (the default of one option of a plan, say), and applying one of
// those at the deadline would record a decision nobody proposed for this question.
func markerDefault(body, slug string) string {
	lines := strings.Split(askTextForSlug(body, slug), "\n")
	if m := inlineDefaultRegex.FindStringSubmatch(lines[0]); len(m) == 2 {
		if d := strings.TrimSpace(strings.Trim(strings.TrimSpace(m[1]), "*")); d != "" {
			return d
		}
	}
	if len(lines) > 1 {
		if m := recommendedDefaultRegex.FindStringSubmatch(strings.Join(lines[1:], "\n")); len(m) == 2 {
			if d := strings.TrimSpace(strings.Trim(strings.TrimSpace(m[1]), "*")); d != "" {
				return d
			}
		}
	}
	return ""
}
