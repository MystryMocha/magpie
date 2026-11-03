package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/provider"
	"gopkg.in/yaml.v3"
)

// ompAdvisorHome is a sandbox home with omp's agent folder and a
// config.yml of the user's in it, and the omp row there.
func ompAdvisorHome(t *testing.T, config string) (home, dir string, a *Agent) {
	t.Helper()
	home = syncHome(t)
	t.Setenv("PI_CONFIG_FILES", "")
	t.Setenv("XDG_DATA_HOME", "")
	dir = filepath.Join(home, ".omp", "agent")
	writeFile(t, filepath.Join(dir, "config.yml"), config)
	return home, dir, omp(home)
}

// ompAdvisorWired is ompAdvisorHome's omp connected to magpie, its model
// on one of magpie's, which Disconnect takes out.
func ompAdvisorWired(t *testing.T, config string) (home, dir string, a *Agent) {
	t.Helper()
	home, dir, _ = ompAdvisorHome(t, config)
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro", "flash"}}); err != nil {
		t.Fatal(err)
	}
	a, err := Find("omp")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if !a.Wired() {
		t.Fatal("omp isn't connected")
	}
	return home, dir, a
}

// ompApply is OmpAdvisorApply as the window sends it: with the version of
// the level it read.
func ompApply(a *Agent, ch OmpAdvisorChange) (*OmpAdvisorView, error) {
	v, err := a.OmpAdvisorView(ch.Project)
	if err != nil {
		return nil, err
	}
	ch.Version = v.User.Version
	if ch.Project != "" {
		ch.Version = v.Project.Version
	}
	return a.OmpAdvisorApply(ch)
}

// ompHasMagpie: omp's models.yml in dir has magpie's provider.
func ompHasMagpie(dir string) bool {
	var m struct{ Providers map[string]any }
	b, _ := os.ReadFile(filepath.Join(dir, "models.yml"))
	yaml.Unmarshal(b, &m)
	return m.Providers["magpie"] != nil
}

// The switch and the other advisor.* settings are written as omp's settings
// read them: a boolean, a number, syncBacklog's "3" a string. A value omp
// doesn't take is refused before anything is written, and the user's own
// keys stay. A boolean of the user's in YAML 1.2's other spellings (True)
// reads as omp reads it.
func TestOmpAdvisorSettingsAsOmpReadsThem(t *testing.T) {
	_, dir, a := ompAdvisorHome(t, "# mine\ntheme: dark\nadvisor:\n  immuneTurns: 2\n  evictStaleResults: FALSE\n")
	cfg := filepath.Join(dir, "config.yml")
	_, err := ompApply(a, OmpAdvisorChange{Settings: map[string]string{
		"advisor.enabled": "true", "advisor.reviewMode": "agent-end", "advisor.syncBacklog": "3", "advisor.maxNotesPerUpdate": "8",
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw := readFile(cfg)
	for _, want := range []string{"# mine", "theme: dark", "immuneTurns: 2", "enabled: true", "reviewMode: agent-end", `syncBacklog: "3"`, "maxNotesPerUpdate: 8"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("config.yml lacks %q:\n%s", want, raw)
		}
	}
	for _, bad := range []map[string]string{
		{"advisor.enabled": "yes"}, {"advisor.maxNotesPerUpdate": "33"}, {"advisor.immuneTurns": "6"},
		{"advisor.reviewMode": "always"}, {"advisor.model": "x"},
	} {
		if _, err := ompApply(a, OmpAdvisorChange{Settings: bad}); err == nil {
			t.Fatalf("%v was taken", bad)
		}
	}
	if readFile(cfg) != raw {
		t.Fatalf("a refused change wrote:\n%s", readFile(cfg))
	}
	v, err := a.OmpAdvisorView("")
	if err != nil {
		t.Fatal(err)
	}
	if v.User.Settings["advisor.syncBacklog"] != "3" || v.Effective.Settings["advisor.continueSeverity"] != (OmpSourced{Value: "concern"}) ||
		v.Effective.Settings["advisor.enabled"].Value != "true" || v.Effective.Settings["advisor.enabled"].From != "~/.omp/agent/config.yml" ||
		v.User.Settings["advisor.evictStaleResults"] != "false" {
		t.Fatalf("view: %+v %+v", v.User.Settings, v.Effective.Settings)
	}
	if b := a.OmpAdvisorBrief(); b == nil || !b.Enabled || b.Roster != "" {
		t.Fatalf("brief %+v", b)
	}
	// True is on, as omp's YAML reads it
	os.WriteFile(cfg, []byte("advisor:\n  enabled: True\n"), 0o600)
	if b := a.OmpAdvisorBrief(); b == nil || !b.Enabled || b.Row != "On" {
		t.Fatalf("brief of True %+v", b)
	}
	if v, _ := a.OmpAdvisorView(""); v.User.Settings["advisor.enabled"] != "true" {
		t.Fatalf("True read as %q", v.User.Settings["advisor.enabled"])
	}
}

// The subagents it runs in and its fallback chain go under task.agentAdvisor
// and retry.fallbackChains.advisor; sent empty, each is taken out, the
// other chains staying.
func TestOmpAdvisorSubagentsAndFallback(t *testing.T) {
	_, dir, a := ompAdvisorHome(t, "retry:\n  fallbackChains:\n    slow: [a/b]\n")
	cfg := filepath.Join(dir, "config.yml")
	chain := []string{"magpie/deepseek/pro", " ", "openai/gpt-6"}
	if _, err := ompApply(a, OmpAdvisorChange{Subagents: map[string]string{"task": "off", "scout": "on"}, Fallback: &chain}); err != nil {
		t.Fatal(err)
	}
	if got := edit.GetYAMLMap(cfg, "task.agentAdvisor"); got["task"] != "off" || got["scout"] != "on" {
		t.Fatalf("agentAdvisor %v", got)
	}
	if got := edit.GetYAMLList(cfg, "retry.fallbackChains.advisor"); !slices.Equal(got, []string{"magpie/deepseek/pro", "openai/gpt-6"}) {
		t.Fatalf("chain %v", got)
	}
	none := []string{}
	if _, err := ompApply(a, OmpAdvisorChange{Subagents: map[string]string{"task": ""}, Fallback: &none}); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetYAMLText(cfg, "retry.fallbackChains.advisor"); ok {
		t.Fatalf("chain left:\n%s", readFile(cfg))
	}
	if got := edit.GetYAMLList(cfg, "retry.fallbackChains.slow"); !slices.Equal(got, []string{"a/b"}) {
		t.Fatalf("slow's chain %v", got)
	}
	if got := edit.GetYAMLMap(cfg, "task.agentAdvisor"); got["task"] != "" || got["scout"] != "on" {
		t.Fatalf("agentAdvisor %v", got)
	}
	if _, err := ompApply(a, OmpAdvisorChange{Subagents: map[string]string{"a.b": "on"}}); err == nil {
		t.Fatal("a name with a dot was taken")
	}
}

// A roster is written as omp's own editor writes it (jb): the issue's
// example comes out key for key, reads back the same, and a roster with
// nothing in it takes the file away. A file with instructions alone, or
// nothing, has a roster of none: an empty list to the window, never null.
func TestOmpAdvisorWatchdogAsOmpWritesIt(t *testing.T) {
	_, dir, a := ompAdvisorHome(t, "")
	yes, no := true, false
	w := OmpWatchdog{Instructions: "所有顾问共用的提示", MaxNotesPerUpdate: 4, Advisors: []OmpWatchdogEntry{
		{Name: "quick", Model: "magpie/opendesign/deepseek-v4.1-flash:max", ReviewInterval: 2, Instructions: "只盯明显的执行错误……\n第二行\n"},
		{Name: "final", Model: "magpie/anthropic/claude-opus-5-5:medium", ReviewMode: "agent-end", SyncBacklog: "3", Enabled: &no,
			Scope: "main", IncludeReasoning: &yes, Tools: []string{}, MaxNotesPerUpdate: 2},
	}}
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &w}); err != nil {
		t.Fatal(err)
	}
	want := `instructions: 所有顾问共用的提示
maxNotesPerUpdate: 4
advisors:
  - name: quick
    model: magpie/opendesign/deepseek-v4.1-flash:max
    instructions: |2
      只盯明显的执行错误……
      第二行
    reviewInterval: 2
  - name: final
    model: magpie/anthropic/claude-opus-5-5:medium
    tools: []
    reviewMode: agent-end
    syncBacklog: "3"
    enabled: false
    maxNotesPerUpdate: 2
    scope: main
    includeReasoning: true
`
	if got := readFile(filepath.Join(dir, "WATCHDOG.yml")); got != want {
		t.Fatalf("WATCHDOG.yml:\n%s\nwant:\n%s", got, want)
	}
	v, err := a.OmpAdvisorView("")
	if err != nil {
		t.Fatal(err)
	}
	if v.User.Watchdog == nil || len(v.User.WatchdogError) > 0 || len(v.User.Watchdog.Advisors) != 2 ||
		v.User.Watchdog.Advisors[0].Instructions != w.Advisors[0].Instructions || v.User.Watchdog.Advisors[1].Tools == nil ||
		v.User.Watchdog.Advisors[0].Tools != nil || *v.User.Watchdog.Advisors[1].Enabled {
		t.Fatalf("read back %+v %v", v.User.Watchdog, v.User.WatchdogError)
	}
	if b := a.OmpAdvisorBrief(); b.Roster != "WATCHDOG.yml" || b.Count != 2 {
		t.Fatalf("brief %+v", b)
	}
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &OmpWatchdog{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "WATCHDOG.yml")); !os.IsNotExist(err) {
		t.Fatalf("an empty roster left its file: %v", err)
	}
	for _, body := range []string{"instructions: look closely\n", "", "# nothing yet\n"} {
		writeFile(t, filepath.Join(dir, "WATCHDOG.yml"), body)
		v, err := a.OmpAdvisorView("")
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v.User.Watchdog)
		if !strings.Contains(string(b), `"advisors":[]`) {
			t.Fatalf("%q reads as %s", body, b)
		}
	}
}

// A WATCHDOG.yml with what a rewrite would lose — a key omp doesn't read,
// an entry omp drops, YAML that doesn't parse — is shown, never written
// over.
func TestOmpAdvisorWatchdogKeepsWhatItCantRead(t *testing.T) {
	_, dir, a := ompAdvisorHome(t, "")
	for _, body := range []string{
		"advisors:\n  - name: a\n    colour: red\n",
		"advisors:\n  - name: a\n    reviewMode: sometimes\n",
		"advisors:\n  - name: a\n    reviewInterval: 2.5\n",
		"advisors: [\n",
	} {
		p := filepath.Join(dir, "WATCHDOG.yml")
		writeFile(t, p, body)
		v, err := a.OmpAdvisorView("")
		if err != nil {
			t.Fatal(err)
		}
		if len(v.User.WatchdogError) == 0 {
			t.Fatalf("%q: no warning", body)
		}
		if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "b"}}}}); err == nil {
			t.Fatalf("%q was written over", body)
		}
		if readFile(p) != body {
			t.Fatalf("%q became %q", body, readFile(p))
		}
	}
	os.Remove(filepath.Join(dir, "WATCHDOG.yml"))
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "x", Tools: []string{"rm"}}}}}); err == nil {
		t.Fatal("a tool omp doesn't have was taken")
	}
}

// A roster reads as omp's schema (Ob) and its YAML read it: True and FALSE
// are booleans; reviewInterval is a whole number (2.0 is 2, 2.5 drops the
// advisor); maxNotesPerUpdate any number (0 is passed over, the advisor
// kept); an empty name a name; a key omp doesn't read leaves the advisor
// running; tools named as omp names them (search is grep, Read read), one
// it doesn't have dropped at run time but kept in the file, so a save of
// the roster keeps it rather than being refused.
func TestOmpAdvisorWatchdogReadAsOmpReadsIt(t *testing.T) {
	w, warnings, err := ompWatchdogParse([]byte(`advisors:
  - name: ""
    enabled: False
    includeReasoning: TRUE
    reviewInterval: 2.0
    maxNotesPerUpdate: 0
    tools: [search, Read, gerp, read]
  - name: half
    reviewInterval: 2.5
  - name: extra
    colour: red
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Advisors) != 2 || w.Advisors[0].Name != "" || w.Advisors[1].Name != "extra" {
		t.Fatalf("advisors %+v", w.Advisors)
	}
	e := w.Advisors[0]
	if e.Enabled == nil || *e.Enabled || e.IncludeReasoning == nil || !*e.IncludeReasoning || e.ReviewInterval != 2 || e.MaxNotesPerUpdate != 0 ||
		!slices.Equal(e.Tools, []string{"grep", "read", "gerp"}) {
		t.Fatalf("first %+v", e)
	}
	var kept, lost []string
	for _, x := range warnings {
		if x.Kept {
			kept = append(kept, x.Error())
		} else {
			lost = append(lost, x.Error())
		}
	}
	if len(kept) != 1 || !strings.Contains(kept[0], "gerp") || len(lost) != 2 ||
		!strings.Contains(lost[0], "advisor 2: reviewInterval") || !strings.Contains(lost[1], "advisor 3: colour") {
		t.Fatalf("kept %q lost %q", kept, lost)
	}

	// in a file: the effective roster runs the advisor with an unread key,
	// and its tools as omp runs them
	_, dir, a := ompAdvisorHome(t, "")
	p := filepath.Join(dir, "WATCHDOG.yml")
	writeFile(t, p, "advisors:\n  - name: a\n    tools: [search, Read, gerp]\n  - name: b\n    tools: [gerp]\n")
	v, err := a.OmpAdvisorView("")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.User.WatchdogError) > 0 || len(v.Effective.Roster) != 2 || !slices.Equal(v.Effective.Roster[0].Tools, []string{"grep", "read"}) ||
		v.Effective.Roster[1].Tools != nil || len(v.Effective.Warnings) != 2 {
		t.Fatalf("view %+v %+v %+v", v.User.WatchdogError, v.Effective.Roster, v.Effective.Warnings)
	}
	// a save of the roster keeps the tool the file had, and takes no new one
	roster := *v.User.Watchdog
	roster.Advisors[1].Instructions = "look"
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &roster}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(p); !strings.Contains(got, "- gerp") || !strings.Contains(got, "- grep") || !strings.Contains(got, "instructions: look") {
		t.Fatalf("WATCHDOG.yml:\n%s", got)
	}
	roster.Advisors[1].Tools = []string{"rm"}
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &roster}); err == nil {
		t.Fatal("rm was taken")
	}
	writeFile(t, p, "advisors:\n  - name: a\n    colour: red\n")
	if v, _ := a.OmpAdvisorView(""); len(v.Effective.Roster) != 1 || v.Effective.Roster[0].Name != "a" {
		t.Fatalf("an advisor with a key omp doesn't read isn't running: %+v", v.Effective.Roster)
	}
}

// What is in effect for a project is merged as omp merges it: the
// project's .omp/config.yml over the user's; rosters user first, then the
// project's from the repository's root in, an advisor of a name a later
// file has taking an earlier one's place; every WATCHDOG.md. Each says its
// file.
func TestOmpAdvisorEffectiveInAProject(t *testing.T) {
	home, dir, a := ompAdvisorHome(t, "advisor:\n  enabled: false\n  reviewMode: agent-end\n")
	writeFile(t, filepath.Join(dir, "WATCHDOG.yml"), "instructions: user's\nadvisors:\n  - name: Quick\n    model: a/m\n  - name: final\n")
	writeFile(t, filepath.Join(dir, "WATCHDOG.md"), "user notes\n")
	repo := filepath.Join(home, "work", "repo")
	sub := filepath.Join(repo, "pkg")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	writeFile(t, filepath.Join(repo, ".omp", "WATCHDOG.yml"), "maxNotesPerUpdate: 2\nadvisors:\n  - name: quick\n    model: b/m\n")
	writeFile(t, filepath.Join(repo, "WATCHDOG.md"), "repo notes\n")
	writeFile(t, filepath.Join(sub, ".omp", "config.yml"), "advisor:\n  enabled: true\n")
	writeFile(t, filepath.Join(sub, "WATCHDOG.yml"), "instructions: pkg's\nadvisors:\n  - name: extra\n")
	// above the repository's root: not read
	writeFile(t, filepath.Join(home, "work", "WATCHDOG.yml"), "advisors:\n  - name: outside\n")

	v, err := a.OmpAdvisorView(sub)
	if err != nil {
		t.Fatal(err)
	}
	e := v.Effective
	if e.Settings["advisor.enabled"] != (OmpSourced{"true", "~/work/repo/pkg/.omp/config.yml"}) ||
		e.Settings["advisor.reviewMode"] != (OmpSourced{"agent-end", "~/.omp/agent/config.yml"}) {
		t.Fatalf("settings %+v", e.Settings)
	}
	var names, from []string
	for _, r := range e.Roster {
		names, from = append(names, r.Name+"="+r.Model), append(from, r.From)
	}
	if !slices.Equal(names, []string{"quick=b/m", "final=", "extra="}) ||
		!slices.Equal(from, []string{"~/work/repo/.omp/WATCHDOG.yml", "~/.omp/agent/WATCHDOG.yml", "~/work/repo/pkg/WATCHDOG.yml"}) {
		t.Fatalf("roster %v from %v", names, from)
	}
	if !slices.Equal(e.Rosters, []string{"~/.omp/agent/WATCHDOG.yml", "~/work/repo/.omp/WATCHDOG.yml", "~/work/repo/pkg/WATCHDOG.yml"}) {
		t.Fatalf("rosters %v", e.Rosters)
	}
	if len(e.Instructions) != 2 || e.Instructions[1] != (OmpSourced{"pkg's", "~/work/repo/pkg/WATCHDOG.yml"}) || e.MaxNotes.Value != "2" {
		t.Fatalf("shared %+v %+v", e.Instructions, e.MaxNotes)
	}
	if !slices.Equal(e.Notes, []string{"~/.omp/agent/WATCHDOG.md", "~/work/repo/WATCHDOG.md"}) {
		t.Fatalf("notes %v", e.Notes)
	}
	if v.Project == nil || v.Project.Settings["advisor.enabled"] != "true" || v.Project.WatchdogFile != "~/work/repo/pkg/.omp/WATCHDOG.yml" {
		t.Fatalf("project %+v", v.Project)
	}
	if _, err := a.OmpAdvisorView(filepath.Join(home, "nowhere")); err == nil {
		t.Fatal("a folder that isn't there was read")
	}
	if _, err := a.OmpAdvisorView("relative/path"); err == nil {
		t.Fatal("a relative folder was read")
	}
}

// The other files omp takes settings from count where omp has them: a
// config file PI_CONFIG_FILES adds, over the user's config.yml; the
// project's .omp/settings.json, under its .omp/config.yml; the legacy
// advisor.subagents, the task subagent's advisor where task.agentAdvisor
// doesn't name it. A subagent is named by its .md's frontmatter.
func TestOmpAdvisorEffectiveFromEveryFile(t *testing.T) {
	home, dir, a := ompAdvisorHome(t, "advisor:\n  subagents: true\n  immuneTurns: 2\n")
	overlay := filepath.Join(home, "team.yml")
	writeFile(t, overlay, "advisor:\n  immuneTurns: 0\n  continueSeverity: blocker\n")
	t.Setenv("PI_CONFIG_FILES", overlay)
	proj := filepath.Join(home, "proj")
	writeFile(t, filepath.Join(proj, ".omp", "settings.json"), `{
  // the team's
  "advisor": {"reviewMode": "agent-end", "enabled": false},
  "advisor.subagents": false,
  "retry": {"fallbackChains": {"advisor": ["a/x", "b/y"]}}
}`)
	writeFile(t, filepath.Join(proj, ".omp", "config.yml"), "advisor:\n  enabled: true\n")
	writeFile(t, filepath.Join(dir, "agents", "rev.md"), "---\nname: code-critic\ndescription: x\n---\nLook.\n")

	v, err := a.OmpAdvisorView("")
	if err != nil {
		t.Fatal(err)
	}
	if v.Effective.Subagents["task"] != (OmpSourced{"on", "~/.omp/agent/config.yml"}) ||
		v.Effective.Settings["advisor.immuneTurns"] != (OmpSourced{"0", "~/team.yml"}) {
		t.Fatalf("user's: %+v %+v", v.Effective.Subagents, v.Effective.Settings)
	}
	if !slices.Contains(v.Subagents, "code-critic") || slices.Contains(v.Subagents, "rev") {
		t.Fatalf("subagents %v", v.Subagents)
	}
	v, err = a.OmpAdvisorView(proj)
	if err != nil {
		t.Fatal(err)
	}
	e := v.Effective
	js := "~/proj/.omp/settings.json"
	if e.Settings["advisor.reviewMode"] != (OmpSourced{"agent-end", js}) || e.Settings["advisor.enabled"] != (OmpSourced{"true", "~/proj/.omp/config.yml"}) ||
		e.Settings["advisor.continueSeverity"] != (OmpSourced{"blocker", "~/team.yml"}) || e.Subagents["task"] != (OmpSourced{"off", js}) ||
		!slices.Equal(e.Fallback.Value, []string{"a/x", "b/y"}) || e.Fallback.From != js {
		t.Fatalf("project's: %+v %+v %+v", e.Settings, e.Subagents, e.Fallback)
	}
}

// A project's config is .omp/config.yml alone: omp reads no config.yaml
// there, so magpie neither shows one nor writes to it.
func TestOmpAdvisorProjectConfigIsYml(t *testing.T) {
	home, _, a := ompAdvisorHome(t, "")
	proj := filepath.Join(home, "proj")
	yamlFile := filepath.Join(proj, ".omp", "config.yaml")
	writeFile(t, yamlFile, "advisor:\n  enabled: true\n")
	v, err := a.OmpAdvisorView(proj)
	if err != nil {
		t.Fatal(err)
	}
	if v.Project.Config != "~/proj/.omp/config.yml" || v.Project.Settings["advisor.enabled"] != "" || v.Effective.Settings["advisor.enabled"].Value != "false" {
		t.Fatalf("project %+v, in effect %+v", v.Project, v.Effective.Settings["advisor.enabled"])
	}
	if _, err := ompApply(a, OmpAdvisorChange{Project: proj, Settings: map[string]string{"advisor.reviewMode": "agent-end"}}); err != nil {
		t.Fatal(err)
	}
	if got := readFile(yamlFile); got != "advisor:\n  enabled: true\n" {
		t.Fatalf("config.yaml written: %q", got)
	}
	if v, _ := edit.GetYAML(filepath.Join(proj, ".omp", "config.yml"), "advisor.reviewMode"); v != "agent-end" {
		t.Fatalf("config.yml %q", v)
	}
}

// A save made on files that changed since the window read them writes
// nothing: the user's edit in between stays.
func TestOmpAdvisorRefusesWhatChangedSince(t *testing.T) {
	_, dir, a := ompAdvisorHome(t, "")
	v, err := a.OmpAdvisorView("")
	if err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(dir, "WATCHDOG.md")
	writeFile(t, md, "the user's, written while the window was open\n")
	notes := "the window's"
	_, err = a.OmpAdvisorApply(OmpAdvisorChange{Notes: &notes, Settings: map[string]string{"advisor.enabled": "true"}, Version: v.User.Version})
	var say OmpSay
	if !errors.As(err, &say) || !say.Stale {
		t.Fatalf("saved over a change: %v", err)
	}
	if readFile(md) != "the user's, written while the window was open\n" {
		t.Fatalf("WATCHDOG.md %q", readFile(md))
	}
	if _, ok := edit.GetYAML(filepath.Join(dir, "config.yml"), "advisor.enabled"); ok {
		t.Fatal("config.yml written")
	}
	if _, err := a.OmpAdvisorApply(OmpAdvisorChange{Notes: &notes}); err == nil {
		t.Fatal("a save with no version was taken")
	}
}

// What can't be read is unknown, not empty: a WATCHDOG.md magpie can't
// read isn't written over, and a record of magpie's writes that doesn't
// parse has nothing more written and Disconnect put back nothing, the
// record kept.
func TestOmpAdvisorUnreadableIsLeft(t *testing.T) {
	_, dir, a := ompAdvisorWired(t, "")
	md := filepath.Join(dir, "WATCHDOG.md")
	// a folder where the file goes: there, and no read gives its bytes
	os.MkdirAll(filepath.Join(md, "x"), 0o755)
	v, err := a.OmpAdvisorView("")
	if err != nil {
		t.Fatal(err)
	}
	if v.User.NotesError == "" {
		t.Fatal("no notes error")
	}
	notes := "mine"
	if _, err := ompApply(a, OmpAdvisorChange{Notes: &notes}); err == nil {
		t.Fatal("an unreadable WATCHDOG.md was written over")
	}
	if _, err := os.Stat(filepath.Join(md, "x")); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(md)

	// what magpie wrote is recorded; a record cut short is unknown
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "a"}}}}); err != nil {
		t.Fatal(err)
	}
	stash(map[string]string{ompAdvisorKey(a): `{"files":{`})
	if _, err := ompApply(a, OmpAdvisorChange{Notes: &notes}); err == nil {
		t.Fatal("written with a record that doesn't parse")
	}
	if _, err := os.Stat(md); !os.IsNotExist(err) {
		t.Fatal("WATCHDOG.md written")
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "WATCHDOG.yml")); err != nil {
		t.Fatal("put back by a record that doesn't parse:", err)
	}
	if stashShared()[ompAdvisorKey(a)] != `{"files":{` {
		t.Fatalf("record %q", stashShared()[ompAdvisorKey(a)])
	}
}

// A file magpie wrote that can't be read at Disconnect is left, and kept
// in the record for a later one; the rest is put back.
func TestOmpAdvisorRestoreLeavesWhatItCantRead(t *testing.T) {
	home, dir, a := ompAdvisorWired(t, "")
	proj := filepath.Join(home, "proj")
	os.MkdirAll(proj, 0o755)
	notes := "look"
	if _, err := ompApply(a, OmpAdvisorChange{Project: proj, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "a"}}}}); err != nil {
		t.Fatal(err)
	}
	md := filepath.Join(proj, ".omp", "WATCHDOG.md")
	os.Remove(md)
	os.MkdirAll(filepath.Join(md, "x"), 0o755)
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "WATCHDOG.yml")); !os.IsNotExist(err) {
		t.Fatal("the WATCHDOG.yml magpie made stayed")
	}
	if _, err := os.Stat(filepath.Join(md, "x")); err != nil {
		t.Fatal("what couldn't be read was taken:", err)
	}
	rec, err := ompAdvisorRead(a)
	if err != nil || len(rec.Files) != 1 || rec.Files[md] == (ompWas{}) {
		t.Fatalf("record %+v %v", rec, err)
	}
}

// Disconnect puts back what magpie wrote of the advisor's while omp was
// connected: a key to what the user had, a file magpie made gone, a file
// it changed as it was, a project's files and the .omp folder it made too;
// a key the user changed since stays theirs. The preview shows the
// project's files, which its copy of the agent folder doesn't hold.
func TestOmpAdvisorRestoredOnDisconnect(t *testing.T) {
	home, dir, a := ompAdvisorWired(t, "advisor:\n  enabled: false\n  immuneTurns: 1\n")
	cfg := filepath.Join(dir, "config.yml")
	writeFile(t, filepath.Join(dir, "WATCHDOG.md"), "mine\n")
	proj := filepath.Join(home, "proj")
	os.MkdirAll(proj, 0o755)
	notes := "look at tests"
	if _, err := ompApply(a, OmpAdvisorChange{
		Settings: map[string]string{"advisor.enabled": "true", "advisor.immuneTurns": "4", "advisor.evictStaleResults": "false"},
		Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "a"}}}, Notes: &notes,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ompApply(a, OmpAdvisorChange{Project: proj, Settings: map[string]string{"advisor.enabled": "false"},
		Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "p"}}}}); err != nil {
		t.Fatal(err)
	}
	// the user's own change after magpie's
	if err := edit.SetYAML(cfg, edit.KV{Path: "advisor.immuneTurns", Value: 5}); err != nil {
		t.Fatal(err)
	}
	beyond := a.ompAdvisorBeyond()
	var shown []string
	for _, c := range beyond {
		shown = append(shown, c.Path)
	}
	if !slices.Contains(shown, "~/proj/.omp/config.yml") || !slices.Contains(shown, "~/proj/.omp/WATCHDOG.yml") {
		t.Fatalf("preview %+v", beyond)
	}
	if _, err := os.Stat(filepath.Join(proj, ".omp", "WATCHDOG.yml")); err != nil {
		t.Fatal("the preview put the project back:", err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetYAML(cfg, "advisor.enabled"); v != "false" {
		t.Fatalf("enabled %q:\n%s", v, readFile(cfg))
	}
	if v, _ := edit.GetYAML(cfg, "advisor.immuneTurns"); v != "5" {
		t.Fatalf("the user's immuneTurns went: %q", v)
	}
	if _, ok := edit.GetYAML(cfg, "advisor.evictStaleResults"); ok {
		t.Fatalf("a key magpie added stayed:\n%s", readFile(cfg))
	}
	if _, err := os.Stat(filepath.Join(dir, "WATCHDOG.yml")); !os.IsNotExist(err) {
		t.Fatal("the WATCHDOG.yml magpie made stayed")
	}
	if got := readFile(filepath.Join(dir, "WATCHDOG.md")); got != "mine\n" {
		t.Fatalf("WATCHDOG.md %q", got)
	}
	if _, err := os.Stat(filepath.Join(proj, ".omp")); !os.IsNotExist(err) {
		t.Fatal("the project's .omp magpie made stayed")
	}
	if stashShared()[ompAdvisorKey(a)] != "" {
		t.Fatal("the record stayed")
	}
}

// Written while omp isn't connected, the advisor's settings are the user's
// own, as an edit by hand is: nothing is recorded, and a Disconnect — of
// omp unconnected, or of omp connected later — leaves them.
func TestOmpAdvisorUnconnectedIsTheUsers(t *testing.T) {
	home, dir, _ := ompAdvisorHome(t, "modelRoles:\n  default: own/model\n")
	if err := provider.Save(provider.Provider{ID: "deepseek", Name: "DeepSeek", Chat: "https://api.deepseek.com/v1", Key: "k", Models: []string{"pro"}}); err != nil {
		t.Fatal(err)
	}
	a, err := Find("omp")
	if err != nil {
		t.Fatal(err)
	}
	if a.Wired() {
		t.Fatal("connected")
	}
	proj := filepath.Join(home, "proj")
	os.MkdirAll(proj, 0o755)
	notes := "the user's"
	if _, err := ompApply(a, OmpAdvisorChange{Settings: map[string]string{"advisor.enabled": "true"},
		Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "a"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ompApply(a, OmpAdvisorChange{Project: proj, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	if r := stashShared()[ompAdvisorKey(a)]; r != "" {
		t.Fatalf("recorded while unconnected: %s", r)
	}
	still := func(when string) {
		t.Helper()
		if v, _ := edit.GetYAML(filepath.Join(dir, "config.yml"), "advisor.enabled"); v != "true" {
			t.Fatalf("%s: enabled %q", when, v)
		}
		if _, err := os.Stat(filepath.Join(dir, "WATCHDOG.yml")); err != nil {
			t.Fatalf("%s: %v", when, err)
		}
		if got := readFile(filepath.Join(proj, ".omp", "WATCHDOG.md")); got != notes {
			t.Fatalf("%s: WATCHDOG.md %q", when, got)
		}
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	still("unconnected")
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	still("connected and disconnected")
}

// Two saves at once each keep their note of what they wrote: the record
// is read and written under the stash's lock.
func TestOmpAdvisorRecordNotesAtOnce(t *testing.T) {
	_, _, a := ompAdvisorWired(t, "")
	ompAdvisorNoteHold = func() { time.Sleep(20 * time.Millisecond) }
	t.Cleanup(func() { ompAdvisorNoteHold = func() {} })
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ompAdvisorNote(a, true, func(r *ompAdvisorRecord) { r.file("f"+string(rune('a'+i)), ompWas{Had: true}) }); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if rec, _ := ompAdvisorRead(a); len(rec.Files) != 4 {
		t.Fatalf("notes lost: %+v", rec.Files)
	}
}

// Every model the advisor names counts as a role's does for magpie's
// provider in models.yml: a subagent's advisor model (task.agentAdvisor)
// and a roster's keep it there when the main model leaves magpie; what
// Disconnect puts back takes it out with the last of them (a fallback
// chain, a roster). A provider renamed takes the roster's models with it,
// and the record's, so Disconnect still knows them as magpie's.
func TestOmpAdvisorModelsKeepMagpie(t *testing.T) {
	_, dir, a := ompAdvisorWired(t, "task:\n  agentAdvisor:\n    scout: magpie/deepseek/flash\n")
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if !ompHasMagpie(dir) {
		t.Fatal("a subagent's advisor on magpie lost magpie's provider")
	}
	os.WriteFile(filepath.Join(dir, "config.yml"), nil, 0o600)
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "q", Model: "magpie/deepseek/flash:high"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Field("model").Set(""); err != nil {
		t.Fatal(err)
	}
	if !ompHasMagpie(dir) {
		t.Fatal("a roster's advisor on magpie lost magpie's provider")
	}
	// the roster alone on magpie has its wiring checked as a role's is
	if err := edit.SetYAML(filepath.Join(dir, "models.yml"), edit.KV{Path: "providers.magpie.baseUrl", Value: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if d := a.Check(); d == "" {
		t.Fatal("a roster on magpie with its provider's address wrong drifts unseen")
	}
	os.Remove(filepath.Join(dir, "WATCHDOG.yml"))
	forget(ompAdvisorKey(a))

	// the fallback chain magpie wrote is the last thing on magpie
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	chain := []string{"magpie/deepseek/flash"}
	if _, err := ompApply(a, OmpAdvisorChange{Fallback: &chain}); err != nil {
		t.Fatal(err)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, ok := edit.GetYAMLText(filepath.Join(dir, "config.yml"), "retry.fallbackChains.advisor"); ok || ompHasMagpie(dir) {
		t.Fatalf("after Disconnect: chain %v, provider %v\n%s", ok, ompHasMagpie(dir), readFile(filepath.Join(dir, "models.yml")))
	}

	// renamed, then disconnected
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "q", Model: "magpie/deepseek/flash:high"}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := RenameProvider("deepseek", "ds"); err != nil {
		t.Fatal(err)
	}
	if got := readFile(filepath.Join(dir, "WATCHDOG.yml")); !strings.Contains(got, "model: magpie/ds/flash:high") {
		t.Fatalf("roster not renamed:\n%s", got)
	}
	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "WATCHDOG.yml")); !os.IsNotExist(err) || ompHasMagpie(dir) {
		t.Fatalf("after a rename and Disconnect: roster %v, provider %v", err, ompHasMagpie(dir))
	}
}

// A roster's models are renamed in place: comments, quoting and the rest
// of the file as they were.
func TestOmpRosterRename(t *testing.T) {
	move := func(v string) string { return strings.Replace(v, "magpie/deepseek/", "magpie/ds/", 1) }
	in := "# team\nadvisors:\n  - name: a # first\n    model: magpie/deepseek/pro:high # fast\n  - name: b\n    model: \"magpie/deepseek/flash\"\n    instructions: magpie/deepseek/ stays\n  - name: c\n    model: own/m\n"
	got, ok := ompRosterRename(in, move)
	want := "# team\nadvisors:\n  - name: a # first\n    model: magpie/ds/pro:high # fast\n  - name: b\n    model: magpie/ds/flash\n    instructions: magpie/deepseek/ stays\n  - name: c\n    model: own/m\n"
	if !ok || got != want {
		t.Fatalf("got %v:\n%s", ok, got)
	}
	if _, ok := ompRosterRename("advisors:\n  - name: c\n    model: own/m\n", move); ok {
		t.Fatal("changed with nothing to move")
	}
}

// A disconnect's dry run (the preview) puts back the agent folder's files in
// its copy, and shows them: a WATCHDOG.yml magpie made goes. A project's
// files, which the copy doesn't hold, are listed too (beyond), and stay.
func TestOmpAdvisorInDisconnectPreview(t *testing.T) {
	home, dir, a := ompAdvisorWired(t, "")
	repo := filepath.Join(home, "work", "repo")
	os.MkdirAll(repo, 0o755)
	if _, err := ompApply(a, OmpAdvisorChange{Watchdog: &OmpWatchdog{Advisors: []OmpWatchdogEntry{{Name: "a"}}}}); err != nil {
		t.Fatal(err)
	}
	notes := "look at the tests\n"
	if _, err := ompApply(a, OmpAdvisorChange{Project: repo, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	changes, err := DisconnectPreview(a, exe)
	if err != nil {
		t.Fatal(err)
	}
	found, project := false, false
	for _, c := range changes {
		found = found || strings.HasSuffix(c.Path, "/agent/WATCHDOG.yml") && c.Removed
		project = project || strings.HasSuffix(filepath.ToSlash(c.Path), "/.omp/WATCHDOG.md") && c.Removed
	}
	if !found || !project {
		t.Fatalf("preview %+v", changes)
	}
	if _, err := os.Stat(filepath.Join(dir, "WATCHDOG.yml")); err != nil {
		t.Fatal("the preview took the real file:", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".omp", "WATCHDOG.md")); err != nil {
		t.Fatal("the preview took the project's file:", err)
	}
}

// The row's brief: a roster with the switch off runs nothing, and says
// so, as the role alone does. Its WATCHDOG.yml is parsed once a change,
// not on each state read.
func TestOmpAdvisorBrief(t *testing.T) {
	_, dir, a := ompAdvisorHome(t, "")
	parses := 0
	was := ompBriefParse
	ompBriefParse = func(b []byte) (OmpWatchdog, error) { parses++; return was(b) }
	t.Cleanup(func() { ompBriefParse = was })
	writeFile(t, filepath.Join(dir, "WATCHDOG.yml"), "advisors:\n  - name: a\n  - name: b\n")
	for range 3 {
		if b := a.OmpAdvisorBrief(); b.Row != "Off · {roster}: {count} advisors" || b.Count != 2 || b.Note != "advisor not enabled" {
			t.Fatalf("roster with the switch off: %+v", b)
		}
	}
	if parses != 1 {
		t.Fatalf("parsed %d times", parses)
	}
	writeFile(t, filepath.Join(dir, "config.yml"), "advisor:\n  enabled: true\n")
	if b := a.OmpAdvisorBrief(); b.Row != "On · {roster}: {count} advisors" || b.Note != "taken over by WATCHDOG.yml" {
		t.Fatalf("roster with the switch on: %+v", b)
	}
}

// A WSL omp's advisor is looked at only while its distro runs: stopped
// since the row was made, nothing of it is read (#3896).
func TestOmpAdvisorInAStoppedDistro(t *testing.T) {
	syncHome(t)
	root := t.TempDir()
	up := true
	d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.omp": true}, Mirrored: true, Running: true,
		up: func() bool { return up }}
	writeFile(t, filepath.Join(root, "home", "me", ".omp", "agent", "WATCHDOG.yml"), "advisors:\n  - name: a\n")
	a := wslAgent(wslKindOf("omp"), d)
	if b := a.OmpAdvisorBrief(); b == nil || b.Count != 1 {
		t.Fatalf("running: %+v", b)
	}
	up = false
	if b := a.OmpAdvisorBrief(); b != nil {
		t.Fatalf("stopped: %+v", b)
	}
	if _, err := a.OmpAdvisorView(""); err == nil {
		t.Fatal("read while stopped")
	}
}

// A folder the system's picker gives is spelt as a WSL omp spells it: one
// on a Windows drive where the distro mounts the drive.
func TestOmpAdvisorFolderInWSL(t *testing.T) {
	d := distro{Name: "Ubuntu", Home: "/home/me", Root: `\\wsl.localhost\Ubuntu`}
	a := ompIn(d.place("omp@wsl:Ubuntu"))
	if got := a.OmpAdvisorFolder(`C:\Users\me\proj`); got != "/mnt/c/Users/me/proj" {
		t.Fatalf("drive: %q", got)
	}
	if got := a.OmpAdvisorFolder(`\\wsl.localhost\Ubuntu\home\me\x`); got != "/home/me/x" {
		t.Fatalf("distro: %q", got)
	}
	d.Drives = map[string]string{"/c": "C:", "/win/c": "C:", "/d": "D:"}
	a = ompIn(d.place("omp@wsl:Ubuntu"))
	if got := a.OmpAdvisorFolder(`C:\Users\me\proj`); got != "/c/Users/me/proj" {
		t.Fatalf("automount root /: %q", got)
	}
	if got := a.OmpAdvisorFolder(`D:\`); got != "/d" {
		t.Fatalf("a drive's root: %q", got)
	}
}

// The projects offered are those omp's sessions ran in: its agent folder's,
// and with $XDG_DATA_HOME set off Windows, the data folder's omp keeps
// them in, a profile's under profiles/<name>.
func TestOmpAdvisorProjectsUnderXDG(t *testing.T) {
	home, _, _ := ompAdvisorHome(t, "")
	xdg := filepath.Join(home, "data")
	t.Setenv("XDG_DATA_HOME", xdg)
	was := ompXDG
	ompXDG = true
	t.Cleanup(func() { ompXDG = was })
	session := func(dir, cwd string) {
		writeFile(t, filepath.Join(dir, "--x--", "s.jsonl"), `{"type":"session","cwd":`+strconvQuote(cwd)+`}`+"\n")
	}
	session(filepath.Join(xdg, "omp", "sessions"), "/work/a")
	session(filepath.Join(xdg, "omp", "profiles", "work", "sessions"), "/work/b")
	os.MkdirAll(filepath.Join(home, ".omp", "profiles", "work", "agent"), 0o755)
	for id, want := range map[string]string{"omp": "/work/a", "omp#work": "/work/b"} {
		a, err := Find(id)
		if err != nil {
			t.Fatal(err)
		}
		v, err := a.OmpAdvisorView("")
		if err != nil {
			t.Fatal(err)
		}
		if len(v.Projects) != 1 || v.Projects[0].Dir != want {
			t.Fatalf("%s: %+v", id, v.Projects)
		}
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// An advisor's thinking levels are those omp offers its model: what
// magpie's models.yml gives a model of magpie's (a Grok with no max was
// offered max), and none for one that doesn't think.
func TestOmpAdvisorLevelsAreItsModels(t *testing.T) {
	_, _, a := ompAdvisorHome(t, "")
	was := ompVersion
	ompVersion = func() string { return "18.15.0" }
	defer func() { ompVersion = was }()
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"sol", "flash"}}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SetModelEfforts("a/sol", []string{"low", "high", "xhigh"}); err != nil {
		t.Fatal(err)
	}
	v, err := a.OmpAdvisorView("")
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Levels[magpieID+"/a/sol"]; !slices.Equal(got, []string{"low", "high", "xhigh"}) {
		t.Errorf("a/sol offers %v, want [low high xhigh]", got)
	}
	if got, ok := v.Levels[magpieID+"/a/flash"]; !ok || slices.Contains(got, "max") {
		t.Errorf("a/flash offers %v (listed %v), want it listed without max", got, ok)
	}
	if b, _ := json.Marshal(v); !strings.Contains(string(b), `"levels":{`) {
		t.Errorf("the view has no levels: %s", b)
	}
}
