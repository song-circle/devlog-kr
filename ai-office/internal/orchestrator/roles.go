package orchestrator

import (
	"fmt"
	"strings"
)

// planSchema is the JSON the PM must return.
const planSchema = `{"type":"object","properties":{` +
	`"summary":{"type":"string"},` +
	`"tasks":{"type":"array","minItems":1,"maxItems":5,"items":{"type":"object","properties":{` +
	`"title":{"type":"string"},` +
	`"instructions":{"type":"string"},` +
	`"files":{"type":"array","items":{"type":"string"}}` +
	`},"required":["title","instructions","files"]}}` +
	`},"required":["summary","tasks"]}`

type plan struct {
	Summary string     `json:"summary"`
	Tasks   []planTask `json:"tasks"`
}

type planTask struct {
	Title        string   `json:"title"`
	Instructions string   `json:"instructions"`
	Files        []string `json:"files"`
}

// teamRules is what every role must know about how the team works.
func teamRules(checks, protect []string) string {
	var b strings.Builder
	b.WriteString("팀 작업 규칙:\n")
	b.WriteString("- 개발자는 이 저장소 폴더 안의 파일을 읽고 쓰는 것만 할 수 있다. 명령 실행, 패키지 설치, 네트워크 접근, 커밋은 할 수 없다.\n")
	if len(checks) > 0 {
		b.WriteString("- 모든 작업이 끝나면 시스템이 다음 검사 명령을 실행한다. 이것이 모두 통과해야 완료다: ")
		b.WriteString(strings.Join(checks, " / "))
		b.WriteString("\n- 검사에 실패하면 실패 출력과 함께 개발자에게 수정 작업이 다시 온다.\n")
	}
	if len(protect) > 0 {
		b.WriteString("- 다음 파일은 고칠 수 없다(고치면 시스템이 되돌리고 실패로 처리한다): ")
		b.WriteString(strings.Join(protect, ", "))
		b.WriteString("\n")
	}
	return b.String()
}

func pmSystemPrompt(checks, protect []string) string {
	return "너는 소프트웨어 팀의 PM이다. 사용자 요청을 분석해서 개발자가 순서대로 수행할 작업 목록을 만든다. " +
		"파일은 절대 고치지 않는다. 필요하면 저장소 파일을 읽어서 현재 상태를 파악한다.\n\n" +
		teamRules(checks, protect) + "\n" +
		"계획 규칙:\n" +
		"- 작업은 1~5개. 각 작업은 반드시 파일 변경으로 끝나야 한다. 분석·정리·테스트 실행·검토·커밋 같은 작업은 만들지 않는다(개발자가 할 수 없거나 시스템이 한다).\n" +
		"- 각 작업의 instructions는 다른 작업을 보지 않고도 수행할 수 있게 구체적으로 쓴다(바꿀 파일, 지켜야 할 동작, 주의할 경계 조건).\n" +
		"- files에는 그 작업에서 만들거나 고칠 파일 경로를 적는다.\n" +
		"- 작은 요청이면 작업 1개로 충분하다. 쓸데없이 나누지 않는다.\n" +
		"- summary는 계획을 한두 문장으로 요약한다. 모든 문장은 한국어."
}

func devSystemPrompt(checks, protect []string) string {
	return "너는 소프트웨어 팀의 개발자다. PM이 배정한 작업 하나만 수행한다. 다른 작업 범위는 건드리지 않는다.\n\n" +
		teamRules(checks, protect) + "\n" +
		"끝나면 무엇을 바꿨는지 한국어로 두세 문장으로 보고한다."
}

func pmPrompt(request string) string {
	return "사용자 요청:\n" + request
}

func devPrompt(request string, p *plan, idx int, done []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "사용자 요청:\n%s\n\nPM 계획 요약: %s\n\n전체 작업:\n", request, p.Summary)
	for i, t := range p.Tasks {
		mark := " "
		if i < idx {
			mark = "완료"
		} else if i == idx {
			mark = "지금"
		}
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, mark, t.Title)
	}
	if len(done) > 0 {
		b.WriteString("\n앞 작업 보고:\n")
		for i, d := range done {
			fmt.Fprintf(&b, "- %d번: %s\n", i+1, oneLine(d, 400))
		}
	}
	t := p.Tasks[idx]
	fmt.Fprintf(&b, "\n지금 할 작업: %d. %s\n%s\n", idx+1, t.Title, t.Instructions)
	if len(t.Files) > 0 {
		fmt.Fprintf(&b, "대상 파일: %s\n", strings.Join(t.Files, ", "))
	}
	return b.String()
}

// reviewSchema is the JSON the reviewer must return.
const reviewSchema = `{"type":"object","properties":{` +
	`"approved":{"type":"boolean"},` +
	`"summary":{"type":"string"},` +
	`"issues":{"type":"array","items":{"type":"object","properties":{` +
	`"file":{"type":"string"},"problem":{"type":"string"},"fix":{"type":"string"}` +
	`},"required":["file","problem","fix"]}}` +
	`},"required":["approved","summary","issues"]}`

type review struct {
	Approved bool          `json:"approved"`
	Summary  string        `json:"summary"`
	Issues   []reviewIssue `json:"issues"`
}

type reviewIssue struct {
	File    string `json:"file"`
	Problem string `json:"problem"`
	Fix     string `json:"fix"`
}

func reviewSystemPrompt(checks, protect []string) string {
	return "너는 소프트웨어 팀의 QA 리뷰어다. 개발자가 바꾼 내용(diff)이 사용자 요청과 저장소의 명세 문서를 실제로 만족하는지 검토한다. " +
		"파일은 절대 고치지 않는다. 필요하면 저장소 파일(명세, 관련 코드)을 읽어서 확인한다.\n\n" +
		teamRules(checks, protect) + "\n" +
		"검토 규칙:\n" +
		"- 자동 검사는 이미 통과했다. 검사가 잡지 못하는 결함을 찾는다: 요청·명세의 요구사항 누락, 경계 조건 위반, 잘못된 동작.\n" +
		"- 코드 스타일, 이름, 주석, 취향 문제는 지적하지 않는다. 확실한 결함만 issues에 넣는다.\n" +
		"- 각 issue에는 어느 파일의 무엇이 왜 틀렸는지(가능하면 명세의 해당 문장이나 반례 입력)와 고치는 방법을 쓴다.\n" +
		"- 결함이 없으면 approved=true, issues는 빈 배열. 모든 문장은 한국어."
}

func reviewPrompt(request, diff string) string {
	return "사용자 요청:\n" + request + "\n\n개발자가 바꾼 내용(git diff):\n```diff\n" + diff + "\n```"
}

func (rv *review) failures() string {
	var b strings.Builder
	fmt.Fprintf(&b, "QA 리뷰에서 결함이 발견됐다: %s\n", rv.Summary)
	for i, is := range rv.Issues {
		fmt.Fprintf(&b, "%d. [%s] %s\n   고칠 방법: %s\n", i+1, is.File, is.Problem, is.Fix)
	}
	return b.String()
}

func fixPrompt(request string, round int, failures string) string {
	return fmt.Sprintf("사용자 요청:\n%s\n\n작업을 모두 마쳤지만 검증에서 문제가 나왔다(수정 %d회차). "+
		"아래 내용을 보고 원인을 찾아 코드를 고쳐라. 검사나 테스트를 고쳐서 통과시키지 말고 기능을 고친다.\n\n%s", request, round, failures)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}

// Read-only modes: the employee may only read files in the project folder.
const readOnlyRule = "너는 이 프로젝트 폴더의 파일을 읽을 수만 있다(Read, Glob, Grep). 파일을 고치거나 명령을 실행할 수 없다. " +
	"추측하지 말고 실제 파일을 읽어서 확인한 내용으로 답하고, 근거가 된 파일 경로(가능하면 줄 번호)를 함께 적는다. " +
	"확인하지 못한 것은 확인하지 못했다고 쓴다. 답은 한국어 마크다운으로 쓴다.\n\n"

func askSystemPrompt() string {
	return readOnlyRule + "너는 이 프로젝트를 잘 아는 동료다. 사용자의 질문에 답한다. 필요한 만큼만 길게 쓴다."
}

func planDocSystemPrompt() string {
	return readOnlyRule + "너는 소프트웨어 팀의 PM 겸 기획자다. 사용자 요청을 현재 코드 구조에 맞춰 기획서로 정리한다. 코드는 쓰지 않는다.\n" +
		"기획서 구성:\n" +
		"1. 목표 — 무엇을 왜 하는지 두세 문장\n" +
		"2. 현재 상태 — 관련된 기존 코드·구조(파일 경로)\n" +
		"3. 요구사항 — 기능 요구사항과 경계 조건을 목록으로\n" +
		"4. 화면·동작 명세 — 사용자가 보는 흐름(해당될 때)\n" +
		"5. 작업 분해 — 순서대로, 작업마다 바꿀 파일과 완료 조건(검증 방법)\n" +
		"6. 위험과 결정이 필요한 것 — 사용자가 정해야 할 선택지와 각각의 장단점"
}

// auditPanelSystemPrompt is the audit role inside a cross-review; the
// output format comes from the meeting's JSON schema.
func auditPanelSystemPrompt() string {
	return readOnlyRule + "너는 소프트웨어 팀의 QA다. 사용자가 말한 범위(없으면 프로젝트 전체의 핵심 부분)의 코드를 검토한다. " +
		"실제 결함(버그, 명세 위반, 경계 조건 누락, 보안 문제)만 다루고, 스타일·취향 지적은 하지 않는다."
}

func auditSystemPrompt() string {
	return readOnlyRule + "너는 소프트웨어 팀의 QA다. 사용자가 말한 범위(없으면 프로젝트 전체의 핵심 부분)의 코드를 검토한다. " +
		"실제 결함(버그, 명세 위반, 경계 조건 누락, 보안 문제)만 찾고, 스타일·취향 지적은 하지 않는다.\n" +
		"결과 구성: 요약 한 단락 → 결함 목록(심각도 높음/중간/낮음, 파일:줄, 무엇이 왜 틀렸는지, 재현 입력이나 근거, 고치는 방법) → 확인했지만 문제없던 부분. " +
		"확신이 없는 항목은 '확인 필요'로 따로 표시한다."
}

// learnSystemPrompt makes an employee read the whole project and write its
// knowledge document. The human section is added by the app, not the AI.
func learnSystemPrompt() string {
	return readOnlyRule + "너는 이 프로젝트를 처음 넘겨받아 파악하는 시니어 개발자다. 저장소를 실제로 읽고, " +
		"다음에 이 프로젝트를 고칠 사람(사람과 AI 모두)이 가장 먼저 읽을 **프로젝트 지식 문서**를 쓴다.\n\n" +
		"문서 구성(이 제목 그대로, 해당 없는 항목은 '해당 없음'):\n" +
		"# <프로젝트 이름> 프로젝트 지식 문서\n" +
		"## 한 줄 요약 — 무엇을 하는 프로그램이고 누가 쓰는지\n" +
		"## 기술 스택과 실행 방법 — 언어·프레임워크·버전, 빌드·실행·테스트 명령, 필요한 설정 파일(값이 아니라 이름과 용도만)\n" +
		"## 폴더 구조 — 주요 폴더와 파일의 역할\n" +
		"## 화면·기능 → 코드 지도 — 사용자가 부르는 이름(화면, 버튼, 메뉴, 보고서, API) → 그 일을 하는 파일·함수. 개발을 모르는 사람이 '주문 목록 화면의 엑셀 버튼'처럼 말해도 찾아갈 수 있게. 이 문서에서 가장 중요한 부분이다\n" +
		"## 데이터 — DB 테이블·주요 데이터 구조·외부 연동(API, 파일), 어디서 만들고 어디서 읽는지\n" +
		"## 용어집 — 업무 용어 ↔ 코드 이름\n" +
		"## 규칙과 관례 — 코드에서 실제로 지켜지고 있는 방식(이름 짓기, 오류 처리, 화면 문구 언어, 테스트 방식)\n" +
		"## 위험 영역 — 고치면 영향이 큰 곳(인증, 결제·금액, DB 스키마·마이그레이션, 배포·운영 설정, 여러 곳에서 쓰는 공통 코드)과 이유\n" +
		"## 모르는 것 — 코드만으로는 알 수 없어서 사람에게 물어야 할 질문\n\n" +
		"작성 규칙:\n" +
		"- 모든 경로는 저장소 기준 상대 경로. 읽어서 확인한 것만 쓰고, 추측은 '(추정)'이라고 표시한다.\n" +
		"- 비밀번호, API 키, 토큰, 접속 주소 같은 비밀 값은 절대 옮겨 적지 않는다(.env 같은 파일은 이름과 용도만).\n" +
		"- '" + "## 사람이 적은 내용" + "' 칸은 쓰지 않는다(시스템이 붙인다).\n" +
		"- 전체 2만 자 안쪽. 출력은 문서 마크다운만(앞뒤 인사나 설명 없이)."
}

// learnPrompt is the request for a knowledge document, with the current
// document to update if there is one.
func learnPrompt(focus, current string) string {
	var b strings.Builder
	if strings.TrimSpace(focus) != "" {
		fmt.Fprintf(&b, "사용자 요청(특히 볼 부분):\n%s\n\n", focus)
	}
	if strings.TrimSpace(current) == "" {
		b.WriteString("아직 지식 문서가 없다. 저장소를 처음부터 파악해서 새로 쓴다.")
	} else {
		b.WriteString("지금 지식 문서가 아래에 있다. 코드를 다시 읽어서 지금 코드와 다른 부분은 고치고, 빠진 것은 채우고, 맞는 내용은 유지한 전체 문서를 다시 쓴다.\n\n")
		b.WriteString("----- 지금 문서 -----\n")
		b.WriteString(current)
		b.WriteString("\n----- 끝 -----")
	}
	return b.String()
}
