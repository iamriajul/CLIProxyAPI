// Package zai implements thinking configuration for Z.AI GLM models.
//
// GLM lanes use effort levels; the effort ladder is shared across the
// Anthropic-budget and OpenAI wire modes, so the OpenAI reasoning_effort
// encoding carries it. Anthropic-route acceptance of the field is assumed
// from the shared ladder (unverified against live Z.AI); disable thinking
// per-request if a lane rejects it.
package zai

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
)

// Applier implements thinking.ProviderApplier for Z.AI models.
type Applier struct {
	openai.Applier
}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Z.AI thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("zai", NewApplier())
}
