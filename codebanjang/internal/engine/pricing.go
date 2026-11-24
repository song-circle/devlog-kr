package engine

import "strings"

// Price is list price in USD per million tokens.
type Price struct {
	Input, Output, CacheRead float64
}

// Cache writes cost a multiple of the input price.
const (
	cacheWrite5mMul = 1.25
	cacheWrite1hMul = 2.0
)

// prices holds Anthropic first-party list prices (checked 2026-11-18).
// Keys are model id prefixes; the longest matching prefix wins.
var prices = map[string]Price{
	"claude-fable-5-2":  {8, 40, 0.20},
	"claude-fable-5-1":  {10, 50, 1.00},
	"claude-fable-5":    {10, 50, 1.00},
	"claude-opus-5-5":   {4, 20, 0.20},
	"claude-opus-5":     {5, 25, 0.50},
	"claude-opus-4":     {5, 25, 0.50},
	"claude-sonnet-5-5": {2, 10, 0.10},
	"claude-sonnet-5":   {2, 10, 0.20},
	"claude-sonnet-4":   {3, 15, 0.30},
	"claude-haiku-5-5":  {0.10, 0.50, 0.01},
	"claude-haiku-4-5":  {1, 5, 0.10},
}

// PriceFor returns the list price for a model id.
func PriceFor(model string) (Price, bool) {
	best, bestLen := Price{}, 0
	for prefix, p := range prices {
		if strings.HasPrefix(model, prefix) && len(prefix) > bestLen {
			best, bestLen = p, len(prefix)
		}
	}
	return best, bestLen > 0
}

// MessageUsage is the token usage of one model response.
type MessageUsage struct {
	Model        string
	Input        int64
	Output       int64
	CacheRead    int64
	CacheWrite5m int64
	CacheWrite1h int64
}

// Cost prices one response. ok is false when the model has no known price.
func (u MessageUsage) Cost() (usd float64, ok bool) {
	p, ok := PriceFor(u.Model)
	if !ok {
		return 0, false
	}
	usd = float64(u.Input)*p.Input +
		float64(u.Output)*p.Output +
		float64(u.CacheRead)*p.CacheRead +
		float64(u.CacheWrite5m)*p.Input*cacheWrite5mMul +
		float64(u.CacheWrite1h)*p.Input*cacheWrite1hMul
	return usd / 1e6, true
}
