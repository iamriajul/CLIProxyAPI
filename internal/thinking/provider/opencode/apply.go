// Package opencode implements thinking configuration for OpenCode Zen Go models.
//
// The Zen gateway speaks OpenAI-compatible protocols, so models use the
// reasoning_effort format with discrete levels.
package opencode

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
)

// Applier implements thinking.ProviderApplier for OpenCode models.
type Applier struct {
	openai.Applier
}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new OpenCode thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("opencode", NewApplier())
	thinking.RegisterProvider("opencode-go", NewApplier())
}
