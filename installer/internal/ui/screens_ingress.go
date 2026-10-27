// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ui

import (
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/ai-on-gke/substrate-gke/installer/internal/snapshot"
	"github.com/ai-on-gke/substrate-gke/installer/internal/theme"
)

// ingressScreen sets the Envoy ingress router's client allowlist, the
// SPIFFE IDs ate-setup is given as ATE_INGRESS_ALLOWED_SPIFFE_IDS. From
// Substrate v0.6.0 ate-setup refuses to deploy the router without one, so the
// step comes right before the control plane is turned on.
//
// `make run` asks for the IDs, prefilled from an exported
// ATE_INGRESS_ALLOWED_SPIFFE_IDS; what is entered replaces that variable for
// the run. `make run-demo` (--demo) sets the counter demos' driver identities
// instead, read-only, and says so, warning when that replaces an exported
// value. Neither path merges lists.
type ingressScreen struct {
	deps    *Deps
	field   textinput.Model
	errText string
}

func newIngressScreen(deps *Deps) *ingressScreen {
	s := &ingressScreen{deps: deps}
	if deps.Setup.DemoMode {
		return s
	}
	value := strings.Join(deps.Setup.IngressAllowedSPIFFEIDs, ",")
	if value == "" {
		value = os.Getenv("ATE_INGRESS_ALLOWED_SPIFFE_IDS")
	}
	s.field = newInput(value, "spiffe://cluster.local/ns/<namespace>/sa/<service-account>")
	s.field.CharLimit = 2000
	s.field.Width = 76
	return s
}

func (s *ingressScreen) Init() tea.Cmd {
	if s.deps.Setup.DemoMode {
		return nil
	}
	return tea.Batch(s.field.Focus(), textinput.Blink)
}

func (s *ingressScreen) CapturesText() bool { return !s.deps.Setup.DemoMode }

func (s *ingressScreen) Hints() []Hint {
	if s.deps.Setup.DemoMode {
		return []Hint{{"enter", "continue"}, {"b", "back"}}
	}
	return []Hint{{"enter", "confirm"}, {"esc", "back"}}
}

func (s *ingressScreen) Update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyMsg)
	if s.deps.Setup.DemoMode {
		if !ok {
			return nil
		}
		switch key.String() {
		case "enter":
			s.deps.Setup.IngressAllowedSPIFFEIDs = append([]string(nil), snapshot.DemoClientSPIFFEIDs...)
			return goNext
		case "b", "esc":
			return goBack
		}
		return nil
	}
	if ok {
		switch key.String() {
		case "enter":
			ids, err := snapshot.ParseSPIFFEIDs(s.field.Value())
			if err != nil {
				s.errText = err.Error()
				return nil
			}
			s.deps.Setup.IngressAllowedSPIFFEIDs = ids
			return goNext
		case "esc":
			return goBack
		}
	}
	s.errText = ""
	var cmd tea.Cmd
	s.field, cmd = s.field.Update(msg)
	return cmd
}

func (s *ingressScreen) View(w int) string {
	var b strings.Builder
	b.WriteString(theme.Title.Render("Allow ingress clients") + "\n")
	wrap := theme.Subtle.Width(max(w-2, 20))
	b.WriteString(wrap.Render("The ingress router admits only clients that present a pod certificate whose SPIFFE ID is on its allowlist, and any listed client may reach any actor. A pod's ID comes from its namespace and service account: spiffe://cluster.local/ns/<namespace>/sa/<service-account>.") + "\n\n")

	exported := os.Getenv("ATE_INGRESS_ALLOWED_SPIFFE_IDS")
	if s.deps.Setup.DemoMode {
		b.WriteString(wrap.Render("This is a demo install. These IDs, the counter demos' driver pods, will be exported as ATE_INGRESS_ALLOWED_SPIFFE_IDS on your behalf:") + "\n")
		for _, id := range snapshot.DemoClientSPIFFEIDs {
			b.WriteString(theme.CommandLine.Render("  "+id) + "\n")
		}
		if exported != "" {
			b.WriteString("\n" + theme.Warning.Width(max(w-2, 20)).Render("Your exported ATE_INGRESS_ALLOWED_SPIFFE_IDS ("+exported+") is replaced for this run. Use `make run` to install with your own IDs.") + "\n")
		}
		b.WriteString("\n" + theme.Subtle.Render("Press [enter] to continue."))
		return b.String()
	}

	b.WriteString(theme.Title.Render("  Client SPIFFE IDs (comma-separated)") + "\n")
	b.WriteString(s.field.View() + "\n\n")
	if s.errText != "" {
		b.WriteString(theme.ErrorPanel.Width(min(w-4, 90)).Render(theme.Bad.Render(s.errText)) + "\n")
		return b.String()
	}
	note := "What you enter is exported as ATE_INGRESS_ALLOWED_SPIFFE_IDS for this run."
	if exported != "" {
		note = "Prefilled from your exported ATE_INGRESS_ALLOWED_SPIFFE_IDS. " + note + " It replaces the exported value."
	}
	b.WriteString(wrap.Render(note))
	return b.String()
}
