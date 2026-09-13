// Package muse implements thinking configuration for Muse Code (Meta) models.
//
// Muse models use the OpenAI-compatible reasoning_effort format with discrete
// levels (minimal/low/medium/high/xhigh).
package muse

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
)

// Applier implements thinking.ProviderApplier for Muse models.
type Applier struct {
	openai.Applier
}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Muse thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("muse", NewApplier())
	thinking.RegisterProvider("muse-code", NewApplier())
}
