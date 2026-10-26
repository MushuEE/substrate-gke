// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ai-on-gke/substrate-gke/installer/internal/execx"
	"github.com/ai-on-gke/substrate-gke/installer/internal/state"
)

// RunbookURL is upstream's upgrade runbook, which the upgrade track prepares
// for and hands over to.
const RunbookURL = "https://github.com/agent-substrate/substrate/blob/main/docs/upgrade.md"

// DefaultUpgradeDir is where the upgrade track keeps the two source trees.
// They live beside the install cache but outside the directories Cleanup
// sweeps, because the runbook runs from them, through the links the hand-over
// makes, for as long as the upgrade takes, rollback included.
func DefaultUpgradeDir() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cannot locate a cache directory for the upgrade trees: %w", err)
	}
	return filepath.Join(dir, "substrate-gke", "upgrades"), nil
}

// UpgradeTrees returns where the installed tree and the new tree go under
// base: one tree per commit. A tree depends on nothing but its commit, so
// clusters share them, and the paths stay short enough to read off a screen
// and paste whole.
func (b *Builder) UpgradeTrees(base string, st *state.Setup) (installed, next string) {
	return filepath.Join(base, "substrate-"+shorten(st.InstalledCommit)),
		filepath.Join(base, "substrate-"+shorten(b.commit))
}

// FetchTrees is the upgrade track's one command: the installer's shallow
// fetch, done for the installed commit and for the new one, into directories
// that stay. A tree already fetched whole is reused; a partial one is fetched
// again.
//
// Trees are shared between clusters and runs, so each is fetched into a
// staging directory beside its final name and published whole with a
// rename, the way the install cache does it: a concurrent run never sees a
// half-fetched tree, and never deletes one from under a fetch in progress.
func (b *Builder) FetchTrees(st *state.Setup, installedDir, nextDir string) execx.Spec {
	fetch := func(stage, dir, repo, commit, missing string) []string {
		q, s := ShellQuote(dir), `"${`+stage+`}"`
		steps := fetchAt(s, repo, commit)
		if missing != "" {
			steps[1] += " || { echo " + ShellQuote(missing) + " >&2; exit 1; }"
		}
		return append(append([]string{
			fmt.Sprintf(`if [ -e %s/%s ]; then echo "Using cached substrate@%s at "%s; else`, q, CompleteMarker, shorten(commit), q),
			fmt.Sprintf(`mkdir -p %s`, ShellQuote(filepath.Dir(dir))),
			fmt.Sprintf(`%s=$(mktemp -d %s)`, stage, ShellQuote(dir+stageInfix+"XXXXXX")),
		}, steps...),
			excludeMarker(s),
			fmt.Sprintf(`: > %s/%s`, s, CompleteMarker),
			// A run that published first has a tree as good as ours.
			fmt.Sprintf(`if [ -e %s/%s ]; then rm -rf %s; echo "Using cached substrate@%s at "%s; else`, q, CompleteMarker, s, shorten(commit), q),
			fmt.Sprintf(`rm -rf %s; mv %s %s; rm -rf %s/"${%s##*/}"`, q, s, q, q, stage),
			fmt.Sprintf(`echo "Fetched substrate@%s into "%s; fi`, shorten(commit), q),
			`fi`,
		)
	}
	// The installed commit was read off the cluster, not resolved against
	// the repository like the new one; a fork or a private hotfix fails here.
	notUpstream := fmt.Sprintf("Commit %s is not in %s: the installed Substrate was built from a fork or a private hotfix. Follow the runbook from a checkout of your own instead.", st.InstalledCommit, b.repo)
	lines := []string{
		"set -euo pipefail",
		`trap 'rm -rf "${stage_installed:-}" "${stage_next:-}"' EXIT`,
		`trap 'exit 130' INT TERM`,
	}
	lines = append(lines, fetch("stage_installed", installedDir, b.repo, st.InstalledCommit, notUpstream)...)
	lines = append(lines, fetch("stage_next", nextDir, b.repo, b.commit, "")...)
	return execx.Spec{
		Label:   "fetch the installed and the new Substrate trees",
		Display: fmt.Sprintf("git fetch substrate@%s (installed) and substrate@%s (new)", shorten(st.InstalledCommit), shorten(b.commit)),
		Argv:    []string{"bash", "-c", strings.Join(lines, "\n")},
		SimLines: []string{
			fmt.Sprintf("Fetched substrate@%s into %s", shorten(st.InstalledCommit), installedDir),
			fmt.Sprintf("Fetched substrate@%s into %s", shorten(b.commit), nextDir),
		},
	}
}

// RecordPath is the record ate-setup wrote of the cluster st names when it
// deployed there: under ATE_RECORD_DIR, or by default the user cache
// directory, one file per kube context. ate-setup fetched the credentials
// itself, so the context is the one gcloud names.
func RecordPath(st *state.Setup) (string, error) {
	dir := os.Getenv("ATE_RECORD_DIR")
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("cannot locate the cache directory ate-setup records installs in: %w", err)
		}
		dir = filepath.Join(base, "ate-setup")
	}
	context := "gke_" + st.ProjectID + "_" + st.Zone + "_" + st.ClusterName
	return filepath.Join(dir, "installs", context+".yaml"), nil
}

var releaseVersion = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(-[0-9A-Za-z.-]+)?$`)

// CheckUpgrade reports whether the runbook supports moving from installed to
// next. Per its table, patch releases of one minor can, and from 1.0 so can
// the next minor. A version that is not a release, such as a build from
// source, is only checked for being a different one.
func CheckUpgrade(installed, next string) error {
	if installed == next {
		return errors.New("that is the version it runs")
	}
	from, to := releaseVersion.FindStringSubmatch(installed), releaseVersion.FindStringSubmatch(next)
	if from == nil || to == nil {
		return nil
	}
	n := func(m []string, i int) int { v, _ := strconv.Atoi(m[i]); return v }
	major, minor := n(from, 1), n(from, 2)
	switch {
	case n(to, 1) < major || n(to, 1) == major && (n(to, 2) < minor || n(to, 2) == minor && n(to, 3) < n(from, 3)):
		return fmt.Errorf("%s is older than the installed %s; the runbook's rollback covers going back", next, installed)
	case n(to, 1) != major:
		return errors.New("a new major release needs a reinstall")
	case major == 0 && n(to, 2) != minor:
		return errors.New("before 1.0, a new minor release needs a reinstall")
	case n(to, 2) > minor+1:
		return fmt.Errorf("minor releases cannot be skipped; upgrade to v%d.%d first", major, minor+1)
	}
	return nil
}

// RecordPrebuilt reports whether the record at path installs pre-built
// images: whether it has a repo under images, read line by line the way the
// runbook's own commands read the record. ok is false when there is no
// record to read.
func RecordPrebuilt(path string) (prebuilt, ok bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	inImages := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case line == "images:":
			inImages = true
		case !strings.HasPrefix(line, " "):
			inImages = false
		case inImages && strings.HasPrefix(strings.TrimSpace(line), "repo:"):
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "repo:")) != "", true
		}
	}
	return false, true
}

// CheckImageSource reports whether the new version comes from the kind of
// images the cluster's record installs. The runbook copies the record into
// new.yaml and changes only its image tag, so it cannot move a cluster
// between pre-built images and a build from source. Without a record there
// is nothing to check yet.
func CheckImageSource(st *state.Setup) error {
	record, err := RecordPath(st)
	if err != nil {
		return nil
	}
	prebuilt, ok := RecordPrebuilt(record)
	switch {
	case !ok || prebuilt == st.Prebuilt():
		return nil
	case prebuilt:
		return errors.New("its record installs pre-built images, and the runbook only moves them to another tag")
	default:
		return errors.New("its record installs a build from source, and the runbook builds the new release the same way")
	}
}

// UpgradeCommands are the runbook's "Before you start" done for the cluster,
// from the trees already fetched and ate-setup's record of the cluster,
// plus the GKE preparation the runbook leaves out. They are pasted into one
// shell once, and every runbook step runs from that shell. They carry no
// comments: zsh, the macOS default, runs a pasted "#" line as a command.
func (b *Builder) UpgradeCommands(st *state.Setup, installedDir, nextDir string) []string {
	next := b.SubstrateVersion(st)
	record, err := RecordPath(st)
	if err != nil {
		record = "<this cluster's ate-setup record>"
	}
	cmds := []string{
		fmt.Sprintf("gcloud container clusters get-credentials %s --location %s --project %s",
			ShellQuote(st.ClusterName), ShellQuote(st.Zone), ShellQuote(st.ProjectID)),
		"mkdir ~/ate-upgrade && cd ~/ate-upgrade",
		"cp " + ShellQuote(record) + " old.yaml",
		"cp " + ShellQuote(record) + " new.yaml",
	}
	if st.Prebuilt() {
		cmds = append(cmds, fmt.Sprintf(`sed -i.bak "s/^  tag: .*/  tag: %s/" new.yaml`, next))
	}
	cmds = append(cmds,
		"ln -s "+ShellQuote(installedDir)+" old",
		"ln -s "+ShellQuote(nextDir)+" new",
		"(cd old && go install ./cmd/kubectl-ate)",
		// Substrate v0.6.0 and later impersonate the snapshot transfer
		// service account; creating it only adds bindings, so it is safe
		// for an older release too. The upgrade track never asks for the
		// bucket; the record names it.
		fmt.Sprintf(`SNAPSHOT_TRANSFER_GSA_NAME=%s go -C %s run . create iam --project-id %s --bucket "$(sed -n 's/^  bucketName: //p' old.yaml)"`,
			ShellQuote(TransferGSAName), ShellQuote(b.SetupGCP), ShellQuote(st.ProjectID)),
		"cd new",
	)
	// A build from source takes its version from VERSION, which the record
	// does not keep; pre-built images take it from the tag in the record,
	// unless a VERSION left in the shell overrides it.
	if st.Prebuilt() {
		return append(cmds, "unset VERSION")
	}
	return append(cmds, "export VERSION="+ShellQuote(next))
}

// UpgradeNotes are what the commands cannot do by themselves: a record this
// machine lacks, and the version a rollback from source runs as.
func (b *Builder) UpgradeNotes(st *state.Setup) []string {
	var notes []string
	record, err := RecordPath(st)
	if _, statErr := os.Stat(record); err != nil || statErr != nil {
		notes = append(notes, fmt.Sprintf("This machine has no ate-setup record of the cluster at %s. Write one from the settings the cluster was installed with (upstream's docs/operator-install.md lists them) before running the commands.", record))
	}
	if !st.Prebuilt() {
		notes = append(notes, fmt.Sprintf("A rollback runs from ~/ate-upgrade/old: export VERSION=%s there first.", ShellQuote(st.InstalledVersion)))
	}
	return notes
}

// UpgradeSummary is the hand-over printed when the installer exits: the
// runbook, the commands, then the notes.
func (b *Builder) UpgradeSummary(st *state.Setup, installedDir, nextDir string) string {
	lines := []string{"Run these commands once, then follow upstream's upgrade runbook from step 1:", "  " + RunbookURL, ""}
	for _, cmd := range b.UpgradeCommands(st, installedDir, nextDir) {
		lines = append(lines, "  "+cmd)
	}
	for _, note := range b.UpgradeNotes(st) {
		lines = append(lines, "", note)
	}
	return strings.Join(lines, "\n")
}
