package mcp

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// reservedPromptTags lists all system prompt XML boundary tags escaped inside untrusted inputs.
var reservedPromptTags = []string{
	"rules_hierarchy",
	"tier_1_global_rules",
	"tier_2_strategy_instructions",
	"tier_3_branch_directives",
	"main_career_profile",
	"target_job_description",
	"custom_instructions",
	"resume_document",
	"extracted_candidate_data",
	"ats_extraction_rules",
	"ats_matching_rules",
	"response_schema",
}

// reservedPromptTagSet enables constant-time lookup of reserved prompt XML boundary tag names.
var reservedPromptTagSet = buildReservedPromptTagSet(reservedPromptTags)

// reservedPromptTagRegex matches reserved prompt XML boundary tags that must be escaped inside untrusted input.
var reservedPromptTagRegex = buildReservedPromptTagRegex(reservedPromptTags)

// tagRegexCache stores compiled custom XML boundary tag regexes keyed by lowercase tag name.
var tagRegexCache = &customTagRegexCache{
	patterns: make(map[string]*regexp.Regexp),
}

// inlinePromptReplacer escapes quotes, backticks, and angle brackets in single-line prompt arguments.
var inlinePromptReplacer = strings.NewReplacer(
	`"`, "&quot;",
	`'`, "&#39;",
	"`", "&#96;",
	"<", "&lt;",
	">", "&gt;",
)

// xmlCommentReplacer neutralizes XML/HTML comment openers and closers inside fenced content.
var xmlCommentReplacer = strings.NewReplacer("<!--", "&lt;!--", "-->", "--&gt;")

// sanitizeInlinePromptValue neutralizes line breaks, control characters, quotes, and XML tags in single-line prompt arguments.
func sanitizeInlinePromptValue(value string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r == '\r' || r == '\n' || r == '\t' || r == '\v' || r == '\f' || r == '\u0085' || r == '\u2028' || r == '\u2029':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, value)
	return strings.TrimSpace(inlinePromptReplacer.Replace(mapped))
}

// customTagRegexCache caches compiled regular expressions for non-reserved XML boundary tags safely across goroutines.
type customTagRegexCache struct {
	mu       sync.RWMutex
	patterns map[string]*regexp.Regexp
}

// buildReservedPromptTagSet constructs a case-folded lookup set from the configured reserved tag list.
func buildReservedPromptTagSet(tags []string) map[string]struct{} {
	set := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		set[strings.ToLower(tag)] = struct{}{}
	}
	return set
}

// buildReservedPromptTagRegex compiles a single regex matching any opening or closing reserved prompt tag.
func buildReservedPromptTagRegex(tags []string) *regexp.Regexp {
	escaped := make([]string, len(tags))
	for i, tag := range tags {
		escaped[i] = regexp.QuoteMeta(tag)
	}
	return regexp.MustCompile(fmt.Sprintf(`(?i)<\s*/?\s*(?:%s)\b[^<>]*>?`, strings.Join(escaped, "|")))
}

// getOrCompile returns a cached case-insensitive regex matching opening or closing tagName instances.
func (c *customTagRegexCache) getOrCompile(tagName string) *regexp.Regexp {
	key := strings.ToLower(tagName)
	c.mu.RLock()
	re, ok := c.patterns[key]
	c.mu.RUnlock()
	if ok {
		return re
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if re, ok := c.patterns[key]; ok {
		return re
	}
	re = regexp.MustCompile(fmt.Sprintf(`(?i)<\s*/?\s*%s\b[^<>]*>?`, regexp.QuoteMeta(tagName)))
	c.patterns[key] = re
	return re
}

// SanitizeXMLFence neutralizes closing and parent prompt tags and wraps content in XML boundary tags.
//
// Parameters:
//   - content: Untrusted client or external job description text.
//   - tagName: Target enclosing XML tag name (e.g., 'target_job_description').
//
// Returns:
//   - Escaped content enclosed within opening and closing XML tags.
func SanitizeXMLFence(content, tagName string) string {
	safe := escapeXMLComments(content)
	safe = escapeXMLMatches(safe, reservedPromptTagRegex)
	if !isReservedPromptTag(tagName) {
		safe = escapeXMLMatches(safe, tagRegexCache.getOrCompile(tagName))
	}
	return fmt.Sprintf("<%s>\n%s\n</%s>", tagName, strings.TrimSpace(safe), tagName)
}

// escapeXMLComments repeatedly escapes XML comment delimiters until neither <!-- nor --> remains.
func escapeXMLComments(content string) string {
	safe := content
	for strings.Contains(safe, "<!--") || strings.Contains(safe, "-->") {
		safe = xmlCommentReplacer.Replace(safe)
	}
	return safe
}

// escapeXMLMatches replaces angle brackets on all regex tag matches with XML entities.
func escapeXMLMatches(content string, tagRegex *regexp.Regexp) string {
	return tagRegex.ReplaceAllStringFunc(content, func(m string) string {
		escaped := strings.ReplaceAll(m, "<", "&lt;")
		return strings.ReplaceAll(escaped, ">", "&gt;")
	})
}

// isReservedPromptTag reports whether tagName is already covered by reservedPromptTagRegex.
func isReservedPromptTag(tagName string) bool {
	_, ok := reservedPromptTagSet[strings.ToLower(tagName)]
	return ok
}
