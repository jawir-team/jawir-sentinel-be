package chunking

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestChunkPolicySections(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantSections []string
		wantContents []string
	}{
		{
			name:         "short policy without headings",
			input:        "  Kebijakan singkat  \r\nberlaku untuk semua.   \r\n",
			wantSections: []string{""},
			wantContents: []string{"  Kebijakan singkat\nberlaku untuk semua."},
		},
		{
			name:         "blank lines normalized",
			input:        "\n\nPertama\n\n\n\nKedua\n\n",
			wantSections: []string{""},
			wantContents: []string{"Pertama\n\n\nKedua"},
		},
		{
			name:         "markdown headings",
			input:        "# Ruang Lingkup\nSemua pegawai.\n\n## Pengecualian\nTidak ada.",
			wantSections: []string{"Ruang Lingkup", "Pengecualian"},
			wantContents: []string{"# Ruang Lingkup\nSemua pegawai.", "## Pengecualian\nTidak ada."},
		},
		{
			name:         "numbered headings",
			input:        "1. Prosedur\nIkuti langkah.\n\n2.1 Sub-bagian\nLakukan pemeriksaan.",
			wantSections: []string{"1. Prosedur", "2.1 Sub-bagian"},
			wantContents: []string{"1. Prosedur\nIkuti langkah.", "2.1 Sub-bagian\nLakukan pemeriksaan."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChunkPolicy(tt.input)
			if len(got) != len(tt.wantSections) {
				t.Fatalf("ChunkPolicy() returned %d chunks, want %d: %#v", len(got), len(tt.wantSections), got)
			}
			for i := range got {
				if got[i].Index != i {
					t.Errorf("chunk %d Index = %d, want %d", i, got[i].Index, i)
				}
				if got[i].Section != tt.wantSections[i] {
					t.Errorf("chunk %d Section = %q, want %q", i, got[i].Section, tt.wantSections[i])
				}
				if got[i].Content != tt.wantContents[i] {
					t.Errorf("chunk %d Content = %q, want %q", i, got[i].Content, tt.wantContents[i])
				}
				if got[i].TokenCount != len(strings.Fields(got[i].Content)) {
					t.Errorf("chunk %d TokenCount = %d, want %d", i, got[i].TokenCount, len(strings.Fields(got[i].Content)))
				}
			}
		})
	}
}

func TestChunkPolicyOversizedSection(t *testing.T) {
	input := strings.Join([]string{
		words("policy", 0, 250),
		words("policy", 250, 250),
		words("policy", 500, 250),
		words("policy", 750, 250),
		words("policy", 1000, 250),
		words("policy", 1250, 250),
	}, "\n\n")

	got := ChunkPolicy(input)
	wantChunks := ceilingWithOverlap(1500)
	if len(got) != wantChunks {
		t.Fatalf("ChunkPolicy() returned %d chunks, want %d", len(got), wantChunks)
	}

	for i, chunk := range got {
		if chunk.Index != i {
			t.Errorf("chunk %d Index = %d, want %d", i, chunk.Index, i)
		}
		if chunk.Section != "" {
			t.Errorf("chunk %d Section = %q, want empty", i, chunk.Section)
		}
		if chunk.TokenCount > MaxChunkTokens {
			t.Errorf("chunk %d TokenCount = %d, exceeds %d", i, chunk.TokenCount, MaxChunkTokens)
		}
		if chunk.TokenCount != len(strings.Fields(chunk.Content)) {
			t.Errorf("chunk %d TokenCount = %d, want %d", i, chunk.TokenCount, len(strings.Fields(chunk.Content)))
		}
	}
	assertOverlap(t, got)
}

func TestChunkPolicyDoesNotOverlapAcrossSections(t *testing.T) {
	sectionOne := strings.Join([]string{
		"# Section One",
		words("alpha", 0, 250),
		words("alpha", 250, 250),
		words("alpha", 500, 250),
	}, "\n\n")
	sectionTwo := strings.Join([]string{
		"# Section Two",
		words("beta", 0, 250),
		words("beta", 250, 250),
		words("beta", 500, 250),
	}, "\n\n")

	got := ChunkPolicy(sectionOne + "\n\n" + sectionTwo)
	firstSectionTwo := -1
	for i, chunk := range got {
		if chunk.Section == "Section Two" {
			firstSectionTwo = i
			break
		}
	}
	if firstSectionTwo < 0 {
		t.Fatal("ChunkPolicy() returned no chunks for Section Two")
	}

	firstWords := strings.Fields(got[firstSectionTwo].Content)
	if len(firstWords) < 3 || firstWords[0] != "#" || firstWords[1] != "Section" || firstWords[2] != "Two" {
		t.Fatalf("first Section Two chunk starts with %v, want its own heading", firstWords[:min(len(firstWords), 3)])
	}
	for _, chunk := range got[firstSectionTwo:] {
		if strings.Contains(chunk.Content, "alpha") {
			t.Fatalf("Section Two chunk contains a token from Section One: %q", chunk.Content)
		}
	}
}

func TestChunkPolicyHardSplitsParagraphAsLastResort(t *testing.T) {
	input := words("short", 0, 50) + "\n\n" + words("long", 0, 700)
	got := ChunkPolicy(input)
	if len(got) < 2 {
		t.Fatalf("ChunkPolicy() returned %d chunks, want multiple chunks", len(got))
	}
	for i, chunk := range got {
		if chunk.TokenCount > MaxChunkTokens {
			t.Errorf("chunk %d TokenCount = %d, exceeds %d", i, chunk.TokenCount, MaxChunkTokens)
		}
	}
	assertOverlap(t, got)
}

func TestChunkPolicyBoundaryAndEmptyInput(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantChunks int
		wantTokens int
	}{
		{name: "empty", input: "", wantChunks: 0},
		{name: "whitespace", input: " \t\r\n\n   \n", wantChunks: 0},
		{name: "exactly 600 tokens", input: words("boundary", 0, MaxChunkTokens), wantChunks: 1, wantTokens: MaxChunkTokens},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ChunkPolicy(tt.input)
			if got == nil {
				t.Fatal("ChunkPolicy() returned a nil slice")
			}
			if len(got) != tt.wantChunks {
				t.Fatalf("ChunkPolicy() returned %d chunks, want %d", len(got), tt.wantChunks)
			}
			if tt.wantChunks == 1 && got[0].TokenCount != tt.wantTokens {
				t.Errorf("TokenCount = %d, want %d", got[0].TokenCount, tt.wantTokens)
			}
		})
	}
}

func TestChunkPolicyIsDeterministic(t *testing.T) {
	input := "Preamble\n\n# First\n" + words("first", 0, 700) + "\n\n2.1 Second\n" + words("second", 0, 700)
	first := ChunkPolicy(input)
	second := ChunkPolicy(input)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("ChunkPolicy() is not deterministic:\nfirst:  %#v\nsecond: %#v", first, second)
	}
}

func assertOverlap(t *testing.T, chunks []ChunkDraft) {
	t.Helper()
	for i := 0; i+1 < len(chunks); i++ {
		left := strings.Fields(chunks[i].Content)
		right := strings.Fields(chunks[i+1].Content)
		if !reflect.DeepEqual(left[len(left)-OverlapTokens:], right[:OverlapTokens]) {
			t.Errorf("chunks %d and %d do not overlap by exactly %d tokens", i, i+1, OverlapTokens)
		}
	}
}

func words(prefix string, start, count int) string {
	result := make([]string, count)
	for i := range result {
		result[i] = fmt.Sprintf("%s%04d", prefix, start+i)
	}
	return strings.Join(result, " ")
}

func ceilingWithOverlap(tokens int) int {
	if tokens <= MaxChunkTokens {
		return 1
	}
	remaining := tokens - MaxChunkTokens
	step := MaxChunkTokens - OverlapTokens
	return 1 + (remaining+step-1)/step
}
