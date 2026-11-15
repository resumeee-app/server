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

// inlinePromptReplacer normalizes line breaks, double quotes, and angle brackets in single-line prompt fields.
var inlinePromptReplacer = strings.NewReplacer("\r", " ", "\n", " ", `"`, `'`, "<", "&lt;", ">", "&gt;")

// sanitizeInlinePromptValue neutralizes line breaks, quotes, and XML tags in single-line prompt arguments.
func sanitizeInlinePromptValue(value string) string {
	return strings.TrimSpace(inlinePromptReplacer.Replace(value))
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
	return regexp.MustCompile(fmt.Sprintf(`(?i)</?\s*(?:%s)\b[^>]*>`, strings.Join(escaped, "|")))
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
	re = regexp.MustCompile(fmt.Sprintf(`(?i)</?\s*%s\b[^>]*>`, regexp.QuoteMeta(tagName)))
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
	safe := escapeXMLMatches(content, reservedPromptTagRegex)
	if !isReservedPromptTag(tagName) {
		safe = escapeXMLMatches(safe, tagRegexCache.getOrCompile(tagName))
	}
	return fmt.Sprintf("<%s>\n%s\n</%s>", tagName, strings.TrimSpace(safe), tagName)
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
