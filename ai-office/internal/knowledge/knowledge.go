// Package knowledge keeps a project's knowledge document: what an employee
// must know about the project before touching it (structure, how to run it,
// which code is behind which screen, terms, risky areas). It lives in the
// repository (.aioffice/knowledge.md) so it travels with the code through
// git and doubles as a handover document for people.
package knowledge

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// RelPath is where the document lives, relative to the repository root.
const RelPath = ".aioffice/knowledge.md"

// HumanHeading starts the section people write. The AI never writes it:
// when a draft is merged, the existing section is carried over verbatim.
const HumanHeading = "## 사람이 적은 내용"

// humanTemplate is the section added when there is none yet.
const humanTemplate = HumanHeading + ` (AI가 고치지 않음)
<!-- 코드만 읽어서는 알 수 없는 것을 적는다. AI가 문서를 다시 써도 이 칸은 그대로 남는다. -->
- 왜 이렇게 만들었는지(결정과 이유):
- 건드리면 안 되는 곳과 이유:
- 운영 환경에서 주의할 점:
`

// MaxPromptChars caps how much of the document goes into each instruction.
const MaxPromptChars = 30000

// Path is the document's location for a repository.
func Path(repo string) string {
	return filepath.Join(repo, filepath.FromSlash(RelPath))
}

// Load reads the document; a missing document is "" without an error.
func Load(repo string) (string, error) {
	b, err := os.ReadFile(Path(repo))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n"), err
}

// Save writes the document, creating .aioffice if needed.
func Save(repo, text string) error {
	p := Path(repo)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	text = strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n") + "\n"
	return os.WriteFile(p, []byte(text), 0o644)
}

// splitHuman returns the document without the human section, and the
// section itself ("" if absent). The section runs from its heading to the
// next level-2 heading or the end.
func splitHuman(text string) (rest, human string) {
	lines := strings.Split(text, "\n")
	start, end := -1, len(lines)
	for i, l := range lines {
		if start < 0 && strings.HasPrefix(l, HumanHeading) {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(l, "## ") {
			end = i
			break
		}
	}
	if start < 0 {
		return text, ""
	}
	human = strings.TrimRight(strings.Join(lines[start:end], "\n"), "\n")
	rest = strings.Join(append(append([]string{}, lines[:start]...), lines[end:]...), "\n")
	return rest, human
}

// Merge builds the document from an AI draft: whatever the draft says in
// a human section is dropped, and the human section of the previous
// document (or an empty template) is put at the end.
func Merge(draft, previous string) string {
	body, _ := splitHuman(stripPreamble(strings.ReplaceAll(draft, "\r\n", "\n")))
	_, human := splitHuman(previous)
	if human == "" {
		human = strings.TrimRight(humanTemplate, "\n")
	}
	return strings.TrimRight(body, "\n") + "\n\n" + human + "\n"
}

// stripPreamble drops what the AI says before the document's title
// ("모든 파일을 확인했습니다. 문서를 작성합니다." and the like), even when
// told not to.
func stripPreamble(text string) string {
	if strings.HasPrefix(text, "# ") {
		return text
	}
	if i := strings.Index(text, "\n# "); i >= 0 {
		return text[i+1:]
	}
	return text
}

// ForPrompt is the document as added to instructions, capped at
// MaxPromptChars runes.
func ForPrompt(text string) string {
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > MaxPromptChars {
		text = string(r[:MaxPromptChars]) + "\n\n(문서가 길어 여기서 잘림. 필요한 내용은 " + RelPath + " 파일을 직접 읽는다.)"
	}
	return text
}
