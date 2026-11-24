package orchestrator

import (
	"fmt"
	"strings"
)

// Request information travels inside the commits as git trailers, so
// anyone who fetches the shared branches can see whose request a change
// was, without a server: each PC keeps its own request records, but every
// PC sees the same git history.
const (
	TrailerRequest    = "Codebanjang-Request"     // request id
	TrailerRequester  = "Codebanjang-Requester"   // who asked for it
	TrailerMode       = "Codebanjang-Mode"        // solo / team / review
	TrailerDeployedBy = "Codebanjang-Deployed-By" // who put it on the development branch
)

// workMessage is the message of a request's work commit: a one-line
// subject, the full request, and the trailers.
func workMessage(id, requester, mode, request string, followUps ...string) string {
	var b strings.Builder
	b.WriteString("코드반장: " + oneLine(request, 60) + "\n\n")
	body := strings.TrimSpace(request)
	if r := []rune(body); len(r) > 2000 {
		body = string(r[:2000]) + "..."
	}
	b.WriteString(body + "\n\n")
	if len(followUps) > 0 {
		b.WriteString("이어서 한 지시:\n")
		for _, f := range followUps {
			b.WriteString("- " + oneLine(f, 300) + "\n")
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "%s: %s\n", TrailerRequest, id)
	if requester = oneLine(requester, 80); requester != "" {
		fmt.Fprintf(&b, "%s: %s\n", TrailerRequester, requester)
	}
	fmt.Fprintf(&b, "%s: %s\n", TrailerMode, mode)
	return b.String()
}

// mergeMessage is the message of the merge that brings the latest shared
// branch into a request before it is deployed.
func mergeMessage(ref, id, deployer string) string {
	msg := fmt.Sprintf("코드반장: 최신 %s 반영\n\n%s: %s\n", ref, TrailerRequest, id)
	if deployer = oneLine(deployer, 80); deployer != "" {
		msg += fmt.Sprintf("%s: %s\n", TrailerDeployedBy, deployer)
	}
	return msg
}
