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
	TrailerRequest    = "AI-Office-Request"     // request id
	TrailerRequester  = "AI-Office-Requester"   // who asked for it
	TrailerMode       = "AI-Office-Mode"        // solo / team / review
	TrailerDeployedBy = "AI-Office-Deployed-By" // who put it on the development branch
)

// workMessage is the message of a request's work commit: a one-line
// subject, the full request, and the trailers.
func workMessage(id, requester, mode, request string) string {
	var b strings.Builder
	b.WriteString("AI Office: " + oneLine(request, 60) + "\n\n")
	body := strings.TrimSpace(request)
	if r := []rune(body); len(r) > 2000 {
		body = string(r[:2000]) + "..."
	}
	b.WriteString(body + "\n\n")
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
	msg := fmt.Sprintf("AI Office: 최신 %s 반영\n\n%s: %s\n", ref, TrailerRequest, id)
	if deployer = oneLine(deployer, 80); deployer != "" {
		msg += fmt.Sprintf("%s: %s\n", TrailerDeployedBy, deployer)
	}
	return msg
}
