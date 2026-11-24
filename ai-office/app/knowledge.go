package main

import (
	"errors"
	"strings"

	"github.com/song-circle/devlog-kr/ai-office/internal/knowledge"
	"github.com/song-circle/devlog-kr/ai-office/internal/workspace"
)

// KnowledgeView is a folder's knowledge document as the settings screen shows it.
type KnowledgeView struct {
	ProjectID int64  `json:"projectId"`
	Text      string `json:"text"`
	Path      string `json:"path"`   // relative to the repository
	Exists    bool   `json:"exists"` // the file is there
	// Git state of the file: "" committed and unchanged, "new" not in git
	// yet, "changed" edited since the last commit. Others get the document
	// only once it is committed and pushed.
	Git   string `json:"git"`
	Chars int    `json:"chars"`
	// Capped is set when the document is longer than what goes into each
	// instruction.
	Capped bool `json:"capped"`
}

func (a *App) knowledgeView(projectID int64) (*KnowledgeView, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	p, err := a.st.Project(projectID)
	if err != nil || !a.inWorkspace(projectID) {
		return nil, errors.New("이 작업공간의 폴더가 아닙니다")
	}
	text, err := knowledge.Load(p.Repo)
	if err != nil {
		return nil, err
	}
	v := &KnowledgeView{ProjectID: projectID, Text: text, Path: knowledge.RelPath, Exists: text != ""}
	v.Chars = len([]rune(text))
	v.Capped = v.Chars > knowledge.MaxPromptChars
	if v.Exists {
		code, _ := workspace.PathStatus(p.Repo, knowledge.RelPath)
		switch {
		case strings.HasPrefix(code, "??"), strings.HasPrefix(code, "A"):
			v.Git = "new"
		case code != "":
			v.Git = "changed"
		}
	}
	return v, nil
}

// Knowledge returns a folder's knowledge document.
func (a *App) Knowledge(projectID int64) (*KnowledgeView, error) {
	return a.knowledgeView(projectID)
}

// SaveKnowledge writes a folder's knowledge document into its repository
// (.aioffice/knowledge.md). It is not committed: the person decides when.
func (a *App) SaveKnowledge(projectID int64, text string) (*KnowledgeView, error) {
	if _, err := a.knowledgeView(projectID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("빈 문서는 저장하지 않습니다")
	}
	p, _ := a.st.Project(projectID)
	if err := knowledge.Save(p.Repo, text); err != nil {
		return nil, err
	}
	return a.knowledgeView(projectID)
}
