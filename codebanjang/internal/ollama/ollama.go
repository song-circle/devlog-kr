// Package ollama talks to a local Ollama server, which serves open models on
// this PC. Claude Code is pointed at it through its Anthropic-compatible API,
// so a local model runs inside the same engine as everyone else.
package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

// BaseURL of the local server (OLLAMA_HOST overrides it).
func BaseURL() string {
	if h := os.Getenv("OLLAMA_HOST"); h != "" {
		if h[0] == ':' || (len(h) > 0 && h[0] >= '0' && h[0] <= '9') {
			return "http://" + h
		}
		return h
	}
	return "http://localhost:11434"
}

// GPU is held by anything that runs a local model, so models take turns on
// the one graphics card instead of being swapped in and out.
var GPU sync.Mutex

// Env makes Claude Code send its requests to Ollama with the given model.
// Every model slot (main and the small helper model) is set to it, so no
// request goes to Anthropic.
func Env(model string) []string {
	return []string{
		"ANTHROPIC_BASE_URL=" + BaseURL(),
		"ANTHROPIC_AUTH_TOKEN=ollama", // required by Claude Code, ignored by Ollama
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_MODEL=" + model,
		"ANTHROPIC_SMALL_FAST_MODEL=" + model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=" + model,
		"ANTHROPIC_DEFAULT_SONNET_MODEL=" + model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL=" + model,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	}
}

// Model is one installed model.
type Model struct {
	Name   string  `json:"name"`
	SizeGB float64 `json:"sizeGb"`
	Family string  `json:"family"`
	Params string  `json:"params"`
	Quant  string  `json:"quant"`
}

// Status of the local server.
type Status struct {
	Running bool    `json:"running"`
	Version string  `json:"version"`
	Models  []Model `json:"models"`
	Error   string  `json:"error,omitempty"`
}

func get(ctx context.Context, path string, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL()+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// Check reports whether Ollama is running and which models it has.
func Check(ctx context.Context) Status {
	var st Status
	var ver struct {
		Version string `json:"version"`
	}
	if err := get(ctx, "/api/version", &ver); err != nil {
		st.Error = "Ollama가 실행 중이 아닙니다 (" + BaseURL() + ")"
		return st
	}
	st.Running, st.Version = true, ver.Version
	var tags struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Details struct {
				Family            string `json:"family"`
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := get(ctx, "/api/tags", &tags); err != nil {
		st.Error = err.Error()
		return st
	}
	st.Models = []Model{}
	for _, m := range tags.Models {
		st.Models = append(st.Models, Model{Name: m.Name, SizeGB: float64(m.Size) / 1e9,
			Family: m.Details.Family, Params: m.Details.ParameterSize, Quant: m.Details.QuantizationLevel})
	}
	return st
}

// Has reports whether a model is installed.
func (s Status) Has(model string) bool {
	for _, m := range s.Models {
		if m.Name == model || m.Name == model+":latest" {
			return true
		}
	}
	return false
}
