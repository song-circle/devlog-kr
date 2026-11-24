package knowledge

import (
	"strings"
	"testing"
)

func TestMergeKeepsHumanSection(t *testing.T) {
	previous := "# 문서\n\n## 구조\n옛 구조\n\n" + HumanHeading + " (AI가 고치지 않음)\n- 결제 모듈은 건드리지 말 것\n\n## 모르는 것\n- 옛 질문\n"
	// The AI rewrote everything and even tried to write the human section.
	draft := "# 문서\n\n## 구조\n새 구조\n\n" + HumanHeading + "\n- AI가 지어낸 메모\n\n## 모르는 것\n- 새 질문\n"
	got := Merge(draft, previous)
	if !strings.Contains(got, "새 구조") || !strings.Contains(got, "새 질문") {
		t.Errorf("draft body lost:\n%s", got)
	}
	if strings.Contains(got, "AI가 지어낸") || strings.Contains(got, "옛 구조") {
		t.Errorf("wrong text kept:\n%s", got)
	}
	if !strings.Contains(got, "결제 모듈은 건드리지 말 것") || strings.Count(got, HumanHeading) != 1 {
		t.Errorf("human section not carried over once:\n%s", got)
	}
	if !strings.HasSuffix(strings.TrimSpace(got), "- 결제 모듈은 건드리지 말 것") {
		t.Errorf("human section should come last:\n%s", got)
	}
}

// Seen in the first real run: a sentence before the title despite the
// instruction to output the document only.
func TestMergeDropsPreamble(t *testing.T) {
	got := Merge("모든 파일을 확인했습니다. 지식 문서를 작성합니다.\n\n# ledger 프로젝트 지식 문서\n\n## 구조\n내용\n", "")
	if !strings.HasPrefix(got, "# ledger 프로젝트 지식 문서") {
		t.Errorf("preamble kept:\n%s", got)
	}
}

func TestMergeAddsTemplate(t *testing.T) {
	got := Merge("# 문서\n\n## 구조\n내용\n", "")
	if !strings.Contains(got, HumanHeading) || !strings.Contains(got, "건드리면 안 되는 곳") {
		t.Errorf("template missing:\n%s", got)
	}
}

func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	if s, err := Load(dir); s != "" || err != nil {
		t.Fatalf("missing doc = %q, %v", s, err)
	}
	if err := Save(dir, "# 문서\r\n내용"); err != nil {
		t.Fatal(err)
	}
	if s, _ := Load(dir); s != "# 문서\n내용\n" {
		t.Errorf("Load = %q", s)
	}
}

func TestForPromptCaps(t *testing.T) {
	long := strings.Repeat("가", MaxPromptChars+10)
	got := ForPrompt(long)
	if !strings.Contains(got, "잘림") || len([]rune(got)) > MaxPromptChars+200 {
		t.Errorf("not capped: %d runes", len([]rune(got)))
	}
}
