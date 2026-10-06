// Package chunking divides policy text into deterministic, ordered drafts for
// downstream indexing.
//
// Tokens are whitespace-separated words, as defined by strings.Fields. This is
// a deterministic approximation for the 600/100 chunking contract; embedding
// providers apply their own tokenizers and have input limits well above 600
// words.
// Oversized sections are packed on paragraph boundaries where possible. A
// paragraph that cannot fit in an available chunk is hard-split on word
// boundaries only as a last resort.
//
// The chunker never persists policy_chunks rows, mutates policy status or
// index_status, or modifies the source content string. It only returns drafts
// for the indexing service (BE-022/BE-053), which is responsible for persisting
// complete embedded rows.
package chunking

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	// MaxChunkTokens is the maximum number of whitespace-separated words in a
	// chunk, including overlap.
	MaxChunkTokens = 600
	// OverlapTokens is the number of words shared by consecutive chunks in the
	// same section.
	OverlapTokens = 100
)

// ChunkDraft is an ordered policy chunk ready for embedding.
type ChunkDraft struct {
	Index      int
	Section    string
	Content    string
	TokenCount int
}

var (
	markdownHeading = regexp.MustCompile(`^#{1,6}\s+\S`)
	// In addition to headings ending in a dot or right parenthesis, accept
	// hierarchical headings without final punctuation (for example, "2.3.1
	// Scope"), as required by the numbered-heading examples in BE-021.
	numberedHeading = regexp.MustCompile(`^([0-9]+[.)]|[0-9]+(\.[0-9]+)+[.)]?)\s+\S`)
)

type section struct {
	heading string
	content string
}

type paragraph struct {
	words []string
}

// ChunkPolicy deterministically splits policy content on section and paragraph
// boundaries. A heading line remains part of its section's content so that
// chunking never discards source text.
func ChunkPolicy(content string) []ChunkDraft {
	drafts := make([]ChunkDraft, 0)
	normalized := normalize(content)
	if normalized == "" {
		return drafts
	}

	for _, sec := range splitSections(normalized) {
		wordCount := len(strings.Fields(sec.content))
		if wordCount <= MaxChunkTokens {
			drafts = append(drafts, ChunkDraft{
				Index:      len(drafts),
				Section:    sec.heading,
				Content:    sec.content,
				TokenCount: wordCount,
			})
			continue
		}

		for _, words := range splitOversizedSection(sec.content) {
			drafts = append(drafts, ChunkDraft{
				Index:      len(drafts),
				Section:    sec.heading,
				Content:    strings.Join(words, " "),
				TokenCount: len(words),
			})
		}
	}

	return drafts
}

func normalize(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = strings.TrimRightFunc(lines[i], unicode.IsSpace)
	}

	first := 0
	for first < len(lines) && lines[first] == "" {
		first++
	}
	last := len(lines)
	for last > first && lines[last-1] == "" {
		last--
	}
	if first == last {
		return ""
	}

	normalized := make([]string, 0, last-first)
	blankLines := 0
	for _, line := range lines[first:last] {
		if line == "" {
			blankLines++
			if blankLines > 2 {
				continue
			}
		} else {
			blankLines = 0
		}
		normalized = append(normalized, line)
	}
	return strings.Join(normalized, "\n")
}

func splitSections(content string) []section {
	lines := strings.Split(content, "\n")
	sections := make([]section, 0)
	current := section{}
	currentLines := make([]string, 0)

	flush := func() {
		currentLines = trimBlankLines(currentLines)
		if len(currentLines) == 0 {
			return
		}
		current.content = strings.Join(currentLines, "\n")
		sections = append(sections, current)
	}

	for _, line := range lines {
		if heading, ok := detectHeading(line); ok {
			flush()
			current = section{heading: heading}
			currentLines = []string{line}
			continue
		}
		currentLines = append(currentLines, line)
	}
	flush()

	return sections
}

func detectHeading(line string) (string, bool) {
	if markdownHeading.MatchString(line) {
		hashes := 0
		for hashes < len(line) && line[hashes] == '#' {
			hashes++
		}
		return strings.TrimSpace(line[hashes:]), true
	}
	if numberedHeading.MatchString(line) {
		return strings.TrimSpace(line), true
	}
	return "", false
}

func trimBlankLines(lines []string) []string {
	first := 0
	for first < len(lines) && lines[first] == "" {
		first++
	}
	last := len(lines)
	for last > first && lines[last-1] == "" {
		last--
	}
	return lines[first:last]
}

func splitOversizedSection(content string) [][]string {
	paragraphs := splitParagraphs(content)
	chunks := make([][]string, 0)
	paragraphIndex := 0
	wordIndex := 0

	for paragraphIndex < len(paragraphs) {
		chunk := make([]string, 0, MaxChunkTokens)
		if len(chunks) > 0 {
			previous := chunks[len(chunks)-1]
			chunk = append(chunk, previous[len(previous)-OverlapTokens:]...)
		}

		newWords := 0
		for paragraphIndex < len(paragraphs) {
			remaining := paragraphs[paragraphIndex].words[wordIndex:]
			capacity := MaxChunkTokens - len(chunk)
			if len(remaining) <= capacity {
				chunk = append(chunk, remaining...)
				newWords += len(remaining)
				paragraphIndex++
				wordIndex = 0
				continue
			}

			if newWords > 0 {
				// Every non-final chunk must contain enough words to supply the
				// next chunk's exact overlap. This only matters when a very short
				// first paragraph precedes a paragraph that does not fit.
				if len(chunk) < OverlapTokens {
					needed := OverlapTokens - len(chunk)
					chunk = append(chunk, remaining[:needed]...)
					wordIndex += needed
				}
				break
			}

			// A paragraph longer than an empty chunk is hard-split as a last
			// resort. After the first chunk, the overlap occupies part of that
			// capacity, so the same fallback also guarantees forward progress
			// when a paragraph cannot fit beside the required overlap.
			chunk = append(chunk, remaining[:capacity]...)
			newWords += capacity
			wordIndex += capacity
			break
		}

		chunks = append(chunks, chunk)
	}

	return chunks
}

func splitParagraphs(content string) []paragraph {
	lines := strings.Split(content, "\n")
	paragraphs := make([]paragraph, 0)
	paragraphLines := make([]string, 0)

	flush := func() {
		if len(paragraphLines) == 0 {
			return
		}
		paragraphs = append(paragraphs, paragraph{
			words: strings.Fields(strings.Join(paragraphLines, "\n")),
		})
		paragraphLines = paragraphLines[:0]
	}

	for _, line := range lines {
		if line == "" {
			flush()
			continue
		}
		paragraphLines = append(paragraphLines, line)
	}
	flush()

	return paragraphs
}
