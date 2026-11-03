package agent

// omp's advisor (#5019, MystryMocha) is a second model that reviews each
// turn of a session and injects notes. Its model is the advisor role
// (omp.go); the rest is here. In config.yml: the switch and the other
// advisor.* settings, which subagents it runs in (task.agentAdvisor) and
// its fallback chain (retry.fallbackChains.advisor). Beside config.yml:
// the roster of advisors (WATCHDOG.yml) and what they look out for
// (WATCHDOG.md). A project has its own of each, over the user's. All of it
// is read and written as omp 18.15.0 does: advisor/settings.ts,
// advisor/config.ts (A0e merges the rosters, jb writes one),
// advisor/watchdog.ts (dl finds the files) and config/settings.ts (a
// project's .omp/config.yml laid over the user's).
//
// What magpie writes here while omp is connected is recorded as it writes
// it (ompAdvisorRecord), with what was there before, and Disconnect puts
// that back where nothing changed it since: the "Changed" list and
// Disconnect and restore cover these files as they do the rest of omp's.
// Written while omp isn't connected, it is the user's own, as an edit by
// hand is: a later connect's Disconnect leaves it.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/tidwall/jsonc"
	"github.com/yetone/magpie/internal/appdir"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/filememo"
	"github.com/yetone/magpie/internal/sessions"
	"gopkg.in/yaml.v3"
)

// OmpSay is something the advisor's window is told, in words the GUI
// translates: Key an English template with {slots}, Vars what goes in
// them, File the file it is about ("" for none). Kept: magpie writing the
// file over keeps what it is about (a tool omp drops), so it holds no
// edit back. As an error it reads in English.
type OmpSay struct {
	Key  string            `json:"key"`
	Vars map[string]string `json:"vars,omitempty"`
	File string            `json:"file,omitempty"`
	Kept bool              `json:"kept,omitempty"`
	// Stale: the files changed since the window read them
	Stale bool `json:"-"`
}

// ompSay is key's message, kv its slots and their values in turn.
func ompSay(key string, kv ...string) OmpSay {
	s := OmpSay{Key: key}
	for i := 0; i+1 < len(kv); i += 2 {
		if s.Vars == nil {
			s.Vars = map[string]string{}
		}
		s.Vars[kv[i]] = kv[i+1]
	}
	return s
}

func (s OmpSay) Error() string {
	out := s.Key
	for k, v := range s.Vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	if s.File != "" {
		out = s.File + ": " + out
	}
	return out
}

// ompAdvisorMu has one Apply, restore or rename of the advisor's files at a
// time: each reads what is there, writes, and records what it did.
var ompAdvisorMu sync.Mutex

// OmpAdvisorSetting is one of omp's advisor.* settings in config.yml as omp
// defines it (advisor/settings.ts): its kind, its default, the values it
// takes and omp's own words for it and each value (ui.label,
// ui.description), which the window shows in its language.
type OmpAdvisorSetting struct {
	Key         string             `json:"key"`
	Kind        string             `json:"kind"` // bool, enum or number
	Default     string             `json:"default"`
	Label       string             `json:"label"`
	Description string             `json:"description"`
	Options     []OmpAdvisorChoice `json:"options,omitempty"`
	// Min and Max bound a number, which may be one its options don't list
	Min int `json:"min,omitempty"`
	Max int `json:"max,omitempty"`
}

// OmpAdvisorChoice is one value a setting offers.
type OmpAdvisorChoice struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// OmpAdvisorSettings are omp's advisor settings, the order #5019 lists
// them in. enabled is off unless set: a model picked for the advisor role
// alone runs no advisor. syncBacklog's values have no words of omp's.
var OmpAdvisorSettings = []OmpAdvisorSetting{
	{Key: "advisor.enabled", Kind: "bool", Default: "false", Label: "Enable Advisor",
		Description: "Pair a second model (assigned to the 'advisor' role) that passively reviews each turn and injects notes."},
	{Key: "advisor.reviewMode", Kind: "enum", Default: "turn", Label: "Advisor Review Mode",
		Description: "Default advisor cadence when no WATCHDOG.yml roster is present. turn reviews every primary turn; agent-end reviews only final yields.",
		Options: []OmpAdvisorChoice{
			{"turn", "Every turn", "Review every primary update (tool-call round)."},
			{"agent-end", "Agent end", "Review only at final yields (once per run)."},
		}},
	{Key: "advisor.reviewInterval", Kind: "number", Default: "1", Label: "Advisor Review Interval", Min: 1, Max: 1000,
		Description: "Default advisor cadence when no WATCHDOG.yml roster is present: review every Nth eligible primary update. 1 = every update. Skipped updates are sent with the next scheduled review.",
		Options: []OmpAdvisorChoice{
			{"1", "Every eligible update", "Default."}, {"2", "Every 2nd", ""}, {"3", "Every 3rd", ""}, {"5", "Every 5th", ""}, {"10", "Every 10th", ""},
		}},
	{Key: "advisor.syncBacklog", Kind: "enum", Default: "off", Label: "Advisor Sync Backlog",
		Description: "Pause main agent until advisor backlog falls below threshold. Numeric values cap wait at 30 seconds; strict waits for all scheduled reviews without a wall-clock cap. Off disables catch-up delays. Abort, failure, and disposal release waits.",
		Options:     []OmpAdvisorChoice{{"off", "off", ""}, {"1", "1", ""}, {"3", "3", ""}, {"5", "5", ""}, {"strict", "strict", ""}}},
	{Key: "advisor.maxNotesPerUpdate", Kind: "number", Default: "4", Label: "Advisor Max Notes Per Update", Min: 1, Max: 32,
		Description: "Maximum non-blocker advice notes accepted per advisor prompt update (1–32; UI offers 1–5 quick picks). Blockers are exempt.",
		Options: []OmpAdvisorChoice{
			{"1", "1 note", "Anti-flood (strict)."}, {"2", "2 notes", ""}, {"3", "3 notes", ""}, {"4", "4 notes", "Frontier reasoning models. Default."}, {"5", "5 notes", ""},
		}},
	{Key: "advisor.continueSeverity", Kind: "enum", Default: "concern", Label: "Advisor Continue Severity",
		Description: "Minimum severity of an advisor note that wakes the agent after it finished with a final answer. Lower-severity notes stay as a card at the end of the transcript and reach the agent with your next message.",
		Options: []OmpAdvisorChoice{
			{"nit", "Nit", "Every note continues the run."},
			{"concern", "Concern", "Concerns and blockers continue the run. Default."},
			{"blocker", "Blocker", "Only blockers continue the run."},
		}},
	{Key: "advisor.immuneTurns", Kind: "number", Default: "3", Label: "Advisor Immune Turns", Min: 0, Max: 5,
		Description: "After an advisor concern or blocker interrupts, route further concerns as non-interrupting asides for this many primary turns. Blockers are exempt from the cooldown.",
		Options: []OmpAdvisorChoice{
			{"0", "0 steps", "No cooldown."}, {"1", "1 step", ""}, {"2", "2 steps", ""}, {"3", "3 steps", "Default."}, {"4", "4 steps", ""}, {"5", "5 steps", ""},
		}},
	{Key: "advisor.evictStaleResults", Kind: "bool", Default: "true", Label: "Advisor Evict Stale Results",
		Description: "Before each review, replace the advisor's read/grep/glob output from older reviews with a short placeholder. The latest review is kept."},
}

// ompAdvisorSetting is the setting of key, nil for none.
func ompAdvisorSetting(key string) *OmpAdvisorSetting {
	for i := range OmpAdvisorSettings {
		if OmpAdvisorSettings[i].Key == key {
			return &OmpAdvisorSettings[i]
		}
	}
	return nil
}

// value is v as the YAML omp's settings read it: a boolean, an integer, or
// one of an enum's strings (syncBacklog's "3" stays a string).
func (s OmpAdvisorSetting) value(v string) (any, error) {
	switch s.Kind {
	case "bool":
		if b, err := strconv.ParseBool(v); err == nil && (v == "true" || v == "false") {
			return b, nil
		}
	case "number":
		if n, err := strconv.Atoi(v); err == nil && n >= s.Min && n <= s.Max {
			return n, nil
		}
	case "enum":
		if slices.ContainsFunc(s.Options, func(o OmpAdvisorChoice) bool { return o.Value == v }) {
			return v, nil
		}
	}
	return nil, ompSay("{value} is not a value omp takes for {key}", "value", v, "key", s.Key)
}

// ompBundledAgents are the subagents omp ships (task/agents.ts), which
// task.agentAdvisor names; the user's own are its agents folders' .md
// files (ompSubagents).
var ompBundledAgents = []string{"task", "scout", "sonic", "reviewer", "security-reviewer", "seance"}

// OmpAdvisorTools are the tools omp lets an advisor of a roster have
// (advisor/config.ts drops any other, with a log line, and an advisor left
// with none of those named has its default); with none named it has read,
// grep, glob and recall.
var OmpAdvisorTools = []string{"read", "bash", "edit", "ast_grep", "ast_edit", "ask", "debug", "ida", "eval", "github", "glob", "grep", "find", "lsp", "checkpoint", "rewind", "context_notes", "new_context", "security_scan", "task", "wait", "todo", "web_search", "write", "memory_edit", "retain", "recall", "reflect", "learn", "manage_skill"}

// ompToolNames are tools named as omp reads the names (tool-names.ts, Ac):
// in lower case, search as grep, each once. A name omp doesn't know stays
// as written.
func ompToolNames(in []string) []string {
	out := []string{}
	for _, t := range in {
		switch l := strings.ToLower(t); {
		case l == "search":
			t = "grep"
		case slices.Contains(OmpAdvisorTools, l) || slices.Contains([]string{"yield", "goal", "think"}, l):
			t = l
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// ompToolsRun are the tools an advisor naming tools runs with (Ub): those
// omp has for an advisor; nil, its default, when it named some and none of
// them is.
func ompToolsRun(tools []string) []string {
	if len(tools) == 0 {
		return tools
	}
	out := slices.DeleteFunc(slices.Clone(tools), func(t string) bool { return !slices.Contains(OmpAdvisorTools, t) })
	if len(out) == 0 {
		return nil
	}
	return out
}

// OmpWatchdog is a WATCHDOG.yml: instructions every advisor of it shares,
// the most notes per review they share, and the roster. A roster that isn't
// empty runs in place of the advisor role's one advisor. Advisors is never
// nil as read: a file with instructions alone has none.
type OmpWatchdog struct {
	Instructions      string             `json:"instructions,omitempty"`
	MaxNotesPerUpdate int                `json:"maxNotesPerUpdate,omitempty"`
	Advisors          []OmpWatchdogEntry `json:"advisors"`
}

// OmpWatchdogEntry is one advisor of a roster (advisor/config.ts, Ob).
// Unset, its model is the advisor role's, it reviews every turn
// (reviewMode turn, reviewInterval 1: not the global advisor.reviewMode
// and advisor.reviewInterval), its syncBacklog is the global one, it runs
// in the main session and in subagents (scope all), and sees the session's
// reasoning. enabled false keeps it, paused.
type OmpWatchdogEntry struct {
	Name  string `json:"name"`
	Model string `json:"model,omitempty"`
	// Tools nil is omp's own set; empty is none
	Tools             []string `json:"tools"`
	ReviewMode        string   `json:"reviewMode,omitempty"`
	ReviewInterval    int      `json:"reviewInterval,omitempty"`
	SyncBacklog       string   `json:"syncBacklog,omitempty"`
	Instructions      string   `json:"instructions,omitempty"`
	Enabled           *bool    `json:"enabled,omitempty"`
	MaxNotesPerUpdate int      `json:"maxNotesPerUpdate,omitempty"`
	Scope             string   `json:"scope,omitempty"`
	IncludeReasoning  *bool    `json:"includeReasoning,omitempty"`
}

// ompEntryKeys are the keys omp reads of a roster's entry
var ompEntryKeys = []string{"name", "model", "tools", "reviewMode", "reviewInterval", "syncBacklog", "instructions", "enabled", "maxNotesPerUpdate", "scope", "includeReasoning"}

// check is what of e omp would refuse (the nth advisor), nil when it takes
// it all: it drops an entry whose any field has the wrong value. A name
// may be empty: omp names its advisor "advisor".
func (e OmpWatchdogEntry) check(nth string) *OmpSay {
	var s OmpSay
	switch {
	case e.ReviewMode != "" && e.ReviewMode != "turn" && e.ReviewMode != "agent-end":
		s = ompSay("advisor {n}: reviewMode {value} is not turn or agent-end", "value", e.ReviewMode)
	case e.ReviewInterval < 0:
		s = ompSay("advisor {n}: reviewInterval is a whole number of 1 or more")
	case e.SyncBacklog != "" && !slices.Contains([]string{"off", "1", "3", "5", "strict"}, e.SyncBacklog):
		s = ompSay("advisor {n}: syncBacklog {value} is not off, 1, 3, 5 or strict", "value", e.SyncBacklog)
	case e.MaxNotesPerUpdate < 0:
		s = ompSay("advisor {n}: maxNotesPerUpdate is 1 or more")
	case e.Scope != "" && e.Scope != "main" && e.Scope != "subagent" && e.Scope != "all":
		s = ompSay("advisor {n}: scope {value} is not main, subagent or all", "value", e.Scope)
	default:
		return nil
	}
	if s.Vars == nil {
		s.Vars = map[string]string{}
	}
	s.Vars["n"] = nth
	return &s
}

// slug is the name omp tells advisors apart by (slugify): a later file's
// advisor of the same slug takes an earlier one's place.
func (e OmpWatchdogEntry) slug() string {
	s := strings.Trim(ompSlugCut.ReplaceAllString(strings.ToLower(e.Name), "-"), "-")
	if s == "" {
		return "advisor"
	}
	return s
}

var ompSlugCut = regexp.MustCompile(`[^a-z0-9]+`)

// ompWatchdogParse reads a WATCHDOG.yml as omp does (Vu), and says what
// omp passes over in it: a field of the wrong type, an entry it drops, a
// key it doesn't read, a tool it doesn't have. Those not Kept are why
// magpie doesn't write over the file: rewriting it would lose them. err is
// a file omp skips whole.
func ompWatchdogParse(b []byte) (w OmpWatchdog, warnings []OmpSay, err error) {
	w.Advisors = []OmpWatchdogEntry{}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return w, nil, ompSay("doesn't parse as YAML, so omp skips it: {err}", "err", err.Error())
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return w, nil, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return w, nil, ompSay("is not a YAML mapping, so omp skips it")
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		k, v := root.Content[i].Value, root.Content[i+1]
		switch k {
		case "instructions":
			if !ompYAMLStr(v) {
				warnings = append(warnings, ompSay("instructions must be text, so omp passes it over"))
				continue
			}
			w.Instructions = v.Value
		case "maxNotesPerUpdate":
			// a number of 1 or more, its whole part; anything else omp
			// passes over without a word, as its own editor drops it
			if n, ok := ompYAMLNumber(v); ok && n >= 1 {
				w.MaxNotesPerUpdate = ompWhole(n)
			}
		case "advisors":
			if v.Kind != yaml.SequenceNode {
				warnings = append(warnings, ompSay("advisors must be a list, so omp passes it over"))
				continue
			}
			for n, item := range v.Content {
				e, ok, said := ompWatchdogEntry(item, strconv.Itoa(n+1))
				warnings = append(warnings, said...)
				if ok {
					w.Advisors = append(w.Advisors, e)
				}
			}
		default:
			warnings = append(warnings, ompSay("{key} is not a key omp reads", "key", k))
		}
	}
	return w, warnings, nil
}

// ompWatchdogEntry is one roster entry as omp's schema takes it (Ob), the
// nth: ok false for one omp drops (a field of the wrong type or value, no
// name), with what it passes over in it. A key it doesn't read leaves the
// entry running, as a tool it doesn't have does.
func ompWatchdogEntry(n *yaml.Node, nth string) (e OmpWatchdogEntry, ok bool, warnings []OmpSay) {
	say := func(key string, kv ...string) OmpSay {
		return ompSay("advisor {n}: "+key, append([]string{"n", nth}, kv...)...)
	}
	if n.Kind != yaml.MappingNode {
		return e, false, []OmpSay{say("is not a mapping, so omp drops it")}
	}
	str := func(v *yaml.Node, k string, to *string) *OmpSay {
		if !ompYAMLStr(v) {
			s := say("{key} must be text, so omp drops this advisor", "key", k)
			return &s
		}
		*to = v.Value
		return nil
	}
	// a YAML 1.2 boolean, as omp's YAML reads it: true, True or TRUE
	boolean := func(v *yaml.Node, k string, to **bool) *OmpSay {
		if v.Kind != yaml.ScalarNode || v.Tag != "!!bool" {
			s := say("{key} must be true or false, so omp drops this advisor", "key", k)
			return &s
		}
		b := strings.EqualFold(v.Value, "true")
		*to = &b
		return nil
	}
	named := false
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		var bad *OmpSay
		switch k {
		case "name":
			bad, named = str(v, k, &e.Name), true
		case "model":
			bad = str(v, k, &e.Model)
		case "reviewMode":
			bad = str(v, k, &e.ReviewMode)
		case "instructions":
			bad = str(v, k, &e.Instructions)
		case "scope":
			bad = str(v, k, &e.Scope)
		case "enabled":
			bad = boolean(v, k, &e.Enabled)
		case "includeReasoning":
			bad = boolean(v, k, &e.IncludeReasoning)
		case "reviewInterval":
			// a whole number (2.5 is dropped, 2.0 is 2) up to a safe integer
			if x, ok := ompYAMLNumber(v); ok && x >= 1 && x == math.Trunc(x) && x <= 1<<53-1 {
				e.ReviewInterval = int(x)
			} else {
				s := say("reviewInterval must be a whole number of 1 or more, so omp drops this advisor")
				bad = &s
			}
		case "maxNotesPerUpdate":
			// any number: one under 1 is passed over, the advisor kept
			if x, ok := ompYAMLNumber(v); !ok {
				s := say("maxNotesPerUpdate must be a number, so omp drops this advisor")
				bad = &s
			} else if x >= 1 {
				e.MaxNotesPerUpdate = ompWhole(x)
			}
		case "syncBacklog":
			// a number 1, 3 or 5 is read as the string
			if v.Kind == yaml.ScalarNode && (v.Tag == "!!str" || v.Tag == "!!int") {
				e.SyncBacklog = v.Value
			} else {
				s := say("syncBacklog must be off, 1, 3, 5 or strict, so omp drops this advisor")
				bad = &s
			}
		case "tools":
			var names []string
			if v.Kind == yaml.SequenceNode {
				for _, t := range v.Content {
					if !ompYAMLStr(t) {
						names = nil
						break
					}
					names = append(names, t.Value)
				}
			}
			if v.Kind != yaml.SequenceNode || len(names) != len(v.Content) {
				s := say("tools must be a list of names, so omp drops this advisor")
				bad = &s
				break
			}
			e.Tools = ompToolNames(names)
			for _, t := range e.Tools {
				if !slices.Contains(OmpAdvisorTools, t) {
					s := say("omp has no tool {tool} for an advisor, and drops it", "tool", t)
					s.Kept = true
					warnings = append(warnings, s)
				}
			}
		default:
			warnings = append(warnings, say("{key} is not a key omp reads", "key", k))
		}
		if bad != nil {
			return e, false, append(warnings, *bad)
		}
	}
	if !named {
		return e, false, append(warnings, say("has no name, so omp drops it"))
	}
	if bad := e.check(nth); bad != nil {
		return e, false, append(warnings, *bad)
	}
	return e, true, warnings
}

// ompYAMLStr: v is a string, as omp's schema takes one.
func ompYAMLStr(v *yaml.Node) bool { return v.Kind == yaml.ScalarNode && v.Tag == "!!str" }

// ompYAMLNumber is a YAML number, ok for a finite one.
func ompYAMLNumber(v *yaml.Node) (float64, bool) {
	if v.Kind != yaml.ScalarNode || (v.Tag != "!!int" && v.Tag != "!!float") {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(v.Value, "_", ""), 64)
	if err != nil {
		if i, err := strconv.ParseInt(v.Value, 0, 64); err == nil {
			return float64(i), true
		}
		return 0, false
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

// ompWhole is a number's whole part as omp truncates it, held to an int.
func ompWhole(f float64) int { return int(math.Min(math.Trunc(f), math.MaxInt32)) }

// ompWatchdogText is w as omp's own roster editor writes it (jb): its keys
// in omp's order, a multi-line instructions as a literal block; "" for a
// roster with nothing in it, whose file omp takes away.
func ompWatchdogText(w OmpWatchdog) string {
	var s []string
	if strings.TrimSpace(w.Instructions) != "" {
		s = ompYAMLBlock(s, "", "instructions", w.Instructions)
	}
	if w.MaxNotesPerUpdate >= 1 {
		s = append(s, "maxNotesPerUpdate: "+strconv.Itoa(w.MaxNotesPerUpdate))
	}
	if len(w.Advisors) > 0 {
		s = append(s, "advisors:")
		for _, a := range w.Advisors {
			s = append(s, "  - name: "+ompYAMLString(a.Name))
			if strings.TrimSpace(a.Model) != "" {
				s = append(s, "    model: "+ompYAMLString(a.Model))
			}
			if a.Tools != nil {
				if len(a.Tools) == 0 {
					s = append(s, "    tools: []")
				} else {
					s = append(s, "    tools:")
					for _, t := range a.Tools {
						s = append(s, "      - "+ompYAMLString(t))
					}
				}
			}
			if a.ReviewMode != "" {
				s = append(s, "    reviewMode: "+ompYAMLString(a.ReviewMode))
			}
			if a.SyncBacklog != "" {
				s = append(s, "    syncBacklog: "+ompYAMLString(a.SyncBacklog))
			}
			if strings.TrimSpace(a.Instructions) != "" {
				s = ompYAMLBlock(s, "    ", "instructions", a.Instructions)
			}
			if a.Enabled != nil {
				s = append(s, "    enabled: "+strconv.FormatBool(*a.Enabled))
			}
			if a.MaxNotesPerUpdate >= 1 {
				s = append(s, "    maxNotesPerUpdate: "+strconv.Itoa(a.MaxNotesPerUpdate))
			}
			if a.ReviewInterval >= 1 {
				s = append(s, "    reviewInterval: "+strconv.Itoa(a.ReviewInterval))
			}
			if a.Scope != "" {
				s = append(s, "    scope: "+ompYAMLString(a.Scope))
			}
			if a.IncludeReasoning != nil {
				s = append(s, "    includeReasoning: "+strconv.FormatBool(*a.IncludeReasoning))
			}
		}
	}
	if len(s) == 0 {
		return ""
	}
	return strings.Join(s, "\n") + "\n"
}

// ompYAMLBlock adds key's text as omp writes it (Wu): one line, quoted where
// it must be, for text of one line or with a line that starts with a space
// or tab; else a literal block indented two, its trailing newlines kept.
func ompYAMLBlock(s []string, indent, key, text string) []string {
	lines := strings.Split(text, "\n")
	if !strings.Contains(text, "\n") || slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") }) {
		return append(s, indent+key+": "+ompYAMLString(text))
	}
	g := strings.ReplaceAll(text, "\r\n", "\n")
	body := strings.TrimRight(g, "\n")
	trail := len(g) - len(body)
	head := map[int]string{0: "|2-", 1: "|2"}[trail]
	if trail > 1 {
		head = "|2+"
	}
	s = append(s, indent+key+": "+head)
	for _, l := range strings.Split(body, "\n") {
		s = append(s, indent+"  "+l)
	}
	for i := 1; i < trail; i++ {
		s = append(s, indent+"  ")
	}
	return s
}

// ompYAMLString is s as a YAML scalar: bare where it reads back as the
// same string (no indicator first, no comment or key inside it), else
// double-quoted (JSON's quoting, which YAML reads).
func ompYAMLString(s string) string {
	if s != "" && s == strings.TrimSpace(s) && !strings.ContainsAny(s, "\n\r\t#") && !strings.Contains(s, ": ") &&
		!strings.HasSuffix(s, ":") && !strings.ContainsRune("-?:,[]{}&*!|>'\"%@`", rune(s[0])) {
		var back map[string]any
		if yaml.Unmarshal([]byte("v: "+s), &back) == nil && back["v"] == s {
			return s
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSuffix(buf.String(), "\n")
}

// ompAdvisorAt is what the advisor settings of one omp row need: its place
// (a distro's for an omp in WSL), its agent folder and the config.yml there
// omp reads (.yml, else .yaml). live, for an omp in WSL, says whether its
// distro runs now (it may have stopped since the row was made); agent is
// the row, whose record says which project files magpie wrote.
type ompAdvisorAt struct {
	at     place
	dir    string
	config string
	live   func() bool
	agent  *Agent
	// entry is magpie's provider in this omp's models.yml, its models'
	// thinking levels the ones omp offers them (Levels)
	entry func() ompProviderEntry
}

// down: o's distro is stopped, and nothing of it is read (#3896).
func (o *ompAdvisorAt) down() bool { return o.at.cold || o.live != nil && !o.live() }

// OmpAdvisorLayer is what one level sets: the user's agent folder, or a
// project. Paths are as the user knows them (~ for home, a distro's own).
type OmpAdvisorLayer struct {
	Config string `json:"config"`
	// ConfigError: its config.yml doesn't parse, so nothing of it is shown
	// and nothing is written to it
	ConfigError string            `json:"configError,omitempty"`
	Settings    map[string]string `json:"settings"`
	// Subagents is task.agentAdvisor: on, off, or a model, by subagent
	Subagents map[string]string `json:"subagents"`
	Fallback  []string          `json:"fallback"`
	// Watchdog is its WATCHDOG.yml, nil while there is none; edits go to
	// WatchdogFile. WatchdogError is what omp would pass over in it that a
	// rewrite would lose, or why it can't be read, and holds edits back
	Watchdog      *OmpWatchdog `json:"watchdog"`
	WatchdogFile  string       `json:"watchdogFile"`
	WatchdogError []OmpSay     `json:"watchdogError,omitempty"`
	Notes         string       `json:"notes"`
	NotesFile     string       `json:"notesFile"`
	NotesExist    bool         `json:"notesExist,omitempty"`
	// NotesError: WATCHDOG.md is there and can't be read, so it isn't
	// written over
	NotesError string `json:"notesError,omitempty"`
	// Version is what the window read here, which a save sends back:
	// files changed since are not written over
	Version string `json:"version"`
}

// OmpSourced is a value in effect and the file it comes from ("" for omp's
// default).
type OmpSourced struct {
	Value string `json:"value"`
	From  string `json:"from,omitempty"`
}

// OmpEffectiveAdvisor is an advisor of the merged roster, and the file
// whose entry it is.
type OmpEffectiveAdvisor struct {
	OmpWatchdogEntry
	From string `json:"from"`
}

// OmpAdvisorEffective is what omp runs, merged as it merges: settings the
// project's config.yml has over the user's; rosters user first, then the
// project's from its outermost folder in, an advisor of a name a later file
// has taking an earlier one's place; every instructions and WATCHDOG.md.
type OmpAdvisorEffective struct {
	Settings  map[string]OmpSourced `json:"settings"`
	Subagents map[string]OmpSourced `json:"subagents"`
	Fallback  OmpSourcedList        `json:"fallback"`
	// Roster empty: the advisor role's one advisor, on the global cadence
	Roster       []OmpEffectiveAdvisor `json:"roster"`
	Instructions []OmpSourced          `json:"instructions,omitempty"`
	MaxNotes     OmpSourced            `json:"maxNotesPerUpdate"`
	Rosters      []string              `json:"rosters,omitempty"` // the WATCHDOG.yml files read
	Notes        []string              `json:"notes,omitempty"`   // the WATCHDOG.md files read
	Warnings     []OmpSay              `json:"warnings,omitempty"`
}

// OmpSourcedList is a list in effect and its file.
type OmpSourcedList struct {
	Value []string `json:"value"`
	From  string   `json:"from,omitempty"`
}

// OmpAdvisorView is the advisor's settings of an omp row: the user's, a
// project's when one is asked for, and what is in effect there.
type OmpAdvisorView struct {
	Settings  []OmpAdvisorSetting `json:"settings"`
	Subagents []string            `json:"subagents"`
	Tools     []string            `json:"tools"`
	User      OmpAdvisorLayer     `json:"user"`
	// Project is the asked-for project's, as omp spells its folder
	Project    *OmpAdvisorLayer `json:"project,omitempty"`
	ProjectDir string           `json:"projectDir,omitempty"`
	// ProjectHome: the project is home, where omp reads no project
	// config.yml (its .omp is the user's), only rosters and notes
	ProjectHome bool                  `json:"projectHome,omitempty"`
	Projects    []sessions.OmpProject `json:"projects"`
	Effective   OmpAdvisorEffective   `json:"effective"`
	// Levels is the thinking levels omp offers for each of magpie's chat
	// models, by its id in omp (magpie/<id>), as magpie's models.yml gives
	// them: none for one that doesn't think. A model of omp's own isn't
	// here, its levels being omp's to know.
	Levels map[string][]string `json:"levels"`
}

// OmpAdvisorChange is what the window changes at one level: only what is
// sent. A setting, subagent or fallback chain sent empty is taken out, so
// omp's default or the user's level holds; a roster with nothing in it and
// notes sent empty take the file away.
type OmpAdvisorChange struct {
	Project   string            `json:"project,omitempty"`
	Settings  map[string]string `json:"settings,omitempty"`
	Subagents map[string]string `json:"subagents,omitempty"`
	Fallback  *[]string         `json:"fallback,omitempty"`
	Watchdog  *OmpWatchdog      `json:"watchdog,omitempty"`
	Notes     *string           `json:"notes,omitempty"`
	// Version is the level's as the window read it (OmpAdvisorLayer)
	Version string `json:"version"`
}

// OmpAdvisorOf is the omp row's advisor settings; an error for another
// agent.
func OmpAdvisorOf(id string) (*Agent, error) {
	a, err := Find(id)
	if err != nil {
		return nil, err
	}
	if a.ompAdvisor == nil {
		return nil, ompSay("{agent} has no advisor settings", "agent", a.Name)
	}
	return a, nil
}

// show is a path magpie opens as the user knows it: ~/… under home, a
// distro's own spelling for one in WSL.
func (o *ompAdvisorAt) show(local string) string {
	if o.at.spell != nil {
		return o.at.native(local)
	}
	if r, err := filepath.Rel(o.at.home, local); err == nil && r != "." && !strings.HasPrefix(r, "..") {
		return "~/" + filepath.ToSlash(r)
	}
	return local
}

// project is a project folder as omp spells it (a distro's /home/me/x)
// where magpie opens it; an error for one that isn't absolute.
func (o *ompAdvisorAt) project(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if o.at.sys != nil {
		if !strings.HasPrefix(dir, "/") {
			return "", ompSay("{dir} is not a folder's full path in the distro", "dir", dir)
		}
		return o.at.open(o.at.sys(dir)), nil
	}
	if !filepath.IsAbs(dir) {
		return "", ompSay("{dir} is not a folder's full path", "dir", dir)
	}
	return filepath.Clean(dir), nil
}

// pick is name.yml in dir, or name.yaml where only that is there, as omp
// picks the file to edit (C0e).
func ompPick(dir, name string) string {
	yml := filepath.Join(dir, name+".yml")
	if _, err := os.Stat(yml); err != nil {
		if _, err := os.Stat(filepath.Join(dir, name+".yaml")); err == nil {
			return filepath.Join(dir, name+".yaml")
		}
	}
	return yml
}

// files are the config.yml, WATCHDOG.yml and WATCHDOG.md a level reads and
// magpie writes: the agent folder's for "", else the project's .omp
// folder's (omp's projectDir). A project's config is config.yml alone:
// omp reads no config.yaml there (settings.ts reads .omp/settings.json,
// then .omp/config.yml).
func (o *ompAdvisorAt) files(project string) (config, watchdog, notes string) {
	if project == "" {
		return o.config, ompPick(o.dir, "WATCHDOG"), filepath.Join(o.dir, "WATCHDOG.md")
	}
	d := filepath.Join(project, ".omp")
	return filepath.Join(d, "config.yml"), ompPick(d, "WATCHDOG"), filepath.Join(d, "WATCHDOG.md")
}

// ompYAMLOK says whether a YAML file can be read, a missing one being
// empty: nil, or why not.
func ompYAMLOK(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return err
	}
	if len(doc.Content) > 0 && doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("not a YAML mapping")
	}
	return nil
}

// layer reads one level.
func (o *ompAdvisorAt) layer(project string) OmpAdvisorLayer {
	config, watchdog, notes := o.files(project)
	l := OmpAdvisorLayer{Config: o.show(config), Settings: map[string]string{}, Subagents: map[string]string{},
		WatchdogFile: o.show(watchdog), NotesFile: o.show(notes)}
	if err := ompYAMLOK(config); err != nil {
		l.ConfigError = err.Error()
	} else {
		for _, s := range OmpAdvisorSettings {
			if v, ok := edit.GetYAML(config, s.Key); ok {
				// a YAML 1.2 boolean, which omp reads True and TRUE as too
				if s.Kind == "bool" && (strings.EqualFold(v, "true") || strings.EqualFold(v, "false")) {
					v = strings.ToLower(v)
				}
				l.Settings[s.Key] = v
			}
		}
		for k, v := range edit.GetYAMLMap(config, "task.agentAdvisor") {
			l.Subagents[k] = v
		}
		l.Fallback = ompChain(config)
	}
	sum := sha256.New()
	json.NewEncoder(sum).Encode([]any{l.ConfigError, l.Settings, l.Subagents, l.Fallback})
	wb, werr := os.ReadFile(watchdog)
	if werr == nil {
		w, warnings, err := ompWatchdogParse(wb)
		if err != nil {
			warnings = append([]OmpSay{err.(OmpSay)}, warnings...)
		}
		l.Watchdog = &w
		for _, x := range warnings {
			if !x.Kept {
				l.WatchdogError = append(l.WatchdogError, x)
			}
		}
	} else if !errors.Is(werr, fs.ErrNotExist) {
		l.WatchdogError = []OmpSay{ompSay("can't be read: {err}", "err", werr.Error())}
	}
	nb, nerr := os.ReadFile(notes)
	if nerr == nil {
		l.Notes, l.NotesExist = string(nb), true
	} else if !errors.Is(nerr, fs.ErrNotExist) {
		l.NotesError = nerr.Error()
	}
	for _, f := range []struct {
		b   []byte
		err error
	}{{wb, werr}, {nb, nerr}} {
		fmt.Fprintf(sum, "\x00%t %d\x00", f.err == nil, len(f.b))
		sum.Write(f.b)
	}
	l.Version = hex.EncodeToString(sum.Sum(nil))[:16]
	return l
}

// ompChain is the advisor's fallback chain in config: a list, or one
// string of models omp splits at commas.
func ompChain(config string) []string {
	if l := edit.GetYAMLList(config, "retry.fallbackChains.advisor"); l != nil {
		return l
	}
	if v, ok := edit.GetYAML(config, "retry.fallbackChains.advisor"); ok && v != "" {
		var out []string
		for _, m := range strings.Split(v, ",") {
			if m = strings.TrimSpace(m); m != "" {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// vcsRoot is the repository a folder is in, as omp's VCS lookup finds it
// for its file search: the nearest folder up with .git, .jj or .hg; "" for
// none.
func vcsRoot(dir string) string {
	for d := dir; ; {
		for _, m := range []string{".git", ".jj", ".hg"} {
			if _, err := os.Stat(filepath.Join(d, m)); err == nil {
				return d
			}
		}
		up := filepath.Dir(d)
		if up == d {
			return ""
		}
		d = up
	}
}

// found are the files of names omp reads for a session in project (dl):
// the agent folder's, then the project's from the outermost folder in, each
// folder's .omp/<name> before <name>, from project up to its repository's
// root (home without one). A folder whose name starts with a dot has only
// its .omp's read.
func (o *ompAdvisorAt) found(project string, names ...string) []string {
	var out []string
	for _, n := range names {
		if _, err := os.Stat(filepath.Join(o.dir, n)); err == nil {
			out = append(out, filepath.Join(o.dir, n))
		}
	}
	if project == "" {
		return out
	}
	stop := vcsRoot(project)
	if stop == "" {
		stop = o.at.home
	}
	var levels [][]string
	for d := project; ; {
		var here []string
		for _, n := range names {
			for _, p := range []string{filepath.Join(d, ".omp", n), filepath.Join(d, n)} {
				if p == filepath.Join(d, n) && strings.HasPrefix(filepath.Base(d), ".") {
					continue
				}
				if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && !slices.Contains(out, p) {
					here = append(here, p)
				}
			}
		}
		levels = append(levels, here)
		up := filepath.Dir(d)
		if strings.EqualFold(d, stop) || up == d {
			break
		}
		d = up
	}
	for i := len(levels) - 1; i >= 0; i-- {
		out = append(out, levels[i]...)
	}
	return out
}

// ompSettingsAt is what one file of settings sets of the advisor's, and the
// file as the window shows it.
type ompSettingsAt struct {
	from      string
	settings  map[string]string
	subagents map[string]string
	fallback  []string
}

// levelOf is a level's config.yml as l read it, with its legacy
// advisor.subagents read as omp moves it: a boolean, the task subagent's
// advisor on or off, unless task.agentAdvisor names that one already.
func (o *ompAdvisorAt) levelOf(l OmpAdvisorLayer, config string) ompSettingsAt {
	out := ompSettingsAt{from: l.Config, settings: l.Settings, subagents: maps.Clone(l.Subagents), fallback: l.Fallback}
	if v, ok := edit.GetYAML(config, "advisor.subagents"); ok && l.ConfigError == "" {
		ompLegacySubagents(out.subagents, v)
	}
	return out
}

func ompLegacySubagents(subs map[string]string, v string) {
	if _, ok := subs["task"]; ok {
		return
	}
	switch strings.ToLower(v) {
	case "true":
		subs["task"] = "on"
	case "false":
		subs["task"] = "off"
	}
}

// ompLevelFile reads the advisor's settings of a settings file other than
// a level's config.yml: a project's .omp/settings.json, a config file
// PI_CONFIG_FILES adds. Keys may be nested or dotted ("advisor.enabled").
func ompLevelFile(path, shown string) (ompSettingsAt, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ompSettingsAt{}, err
	}
	if strings.HasSuffix(path, ".json") {
		b = jsonc.ToJSON(b)
	}
	var m map[string]any
	if err := yaml.Unmarshal(b, &m); err != nil {
		return ompSettingsAt{}, err
	}
	get := func(key string) (any, bool) {
		if v, ok := m[key]; ok {
			return v, true
		}
		var cur any = m
		for _, p := range strings.Split(key, ".") {
			mm, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			if cur, ok = mm[p]; !ok {
				return nil, false
			}
		}
		return cur, true
	}
	text := func(v any) (string, bool) {
		switch x := v.(type) {
		case string:
			return x, true
		case bool:
			return strconv.FormatBool(x), true
		case int:
			return strconv.Itoa(x), true
		case float64:
			return strconv.FormatFloat(x, 'f', -1, 64), true
		}
		return "", false
	}
	l := ompSettingsAt{from: shown, settings: map[string]string{}, subagents: map[string]string{}}
	for _, s := range OmpAdvisorSettings {
		if v, ok := get(s.Key); ok {
			if t, ok := text(v); ok {
				l.settings[s.Key] = t
			}
		}
	}
	if subs, ok := get("task.agentAdvisor"); ok {
		if mm, ok := subs.(map[string]any); ok {
			for k, v := range mm {
				if t, ok := text(v); ok {
					l.subagents[k] = t
				}
			}
		}
	}
	if v, ok := get("advisor.subagents"); ok {
		if t, ok := text(v); ok {
			ompLegacySubagents(l.subagents, t)
		}
	}
	if v, ok := get("retry.fallbackChains.advisor"); ok {
		switch x := v.(type) {
		case []any:
			l.fallback = []string{}
			for _, m := range x {
				if t, ok := text(m); ok {
					l.fallback = append(l.fallback, t)
				}
			}
		case string:
			for _, m := range strings.Split(x, ",") {
				if m = strings.TrimSpace(m); m != "" {
					l.fallback = append(l.fallback, m)
				}
			}
		}
	}
	return l, nil
}

// overlays are the config files PI_CONFIG_FILES adds to an omp run here,
// as omp resolves them: ~ is home, a relative one is under the project
// omp runs in (skipped for the user's level, which has none). An omp in
// WSL reads the distro's variables, not these.
func (o *ompAdvisorAt) overlays(project string) []string {
	if o.at.spell != nil {
		return nil
	}
	var out []string
	for _, f := range filepath.SplitList(appdir.Getenv("PI_CONFIG_FILES")) {
		if f == "" {
			continue
		}
		if f == "~" || strings.HasPrefix(f, "~/") || strings.HasPrefix(f, `~\`) {
			f = filepath.Join(o.at.home, f[1:])
		}
		if !filepath.IsAbs(f) {
			if project == "" {
				continue
			}
			f = filepath.Join(project, f)
		}
		out = append(out, filepath.Clean(f))
	}
	return out
}

// effective is what omp runs for a session in project ("" for the user's
// alone).
func (o *ompAdvisorAt) effective(project string, user OmpAdvisorLayer, proj *OmpAdvisorLayer) OmpAdvisorEffective {
	e := OmpAdvisorEffective{Settings: map[string]OmpSourced{}, Subagents: map[string]OmpSourced{}, Roster: []OmpEffectiveAdvisor{}}
	for _, s := range OmpAdvisorSettings {
		e.Settings[s.Key] = OmpSourced{Value: s.Default}
	}
	// the settings in order, a later one over an earlier: the user's
	// config.yml, the files PI_CONFIG_FILES adds, then the project's
	// .omp/settings.json and .omp/config.yml
	levels := []ompSettingsAt{o.levelOf(user, o.config)}
	for _, f := range o.overlays(project) {
		l, err := ompLevelFile(f, o.show(f))
		if err != nil {
			e.Warnings = append(e.Warnings, OmpSay{Key: "can't be read: {err}", Vars: map[string]string{"err": err.Error()}, File: o.show(f)})
			continue
		}
		levels = append(levels, l)
	}
	// in home omp reads no project settings (ll): ~/.omp is the user's
	if proj != nil && !strings.EqualFold(filepath.Clean(project), filepath.Clean(o.at.home)) {
		js := filepath.Join(project, ".omp", "settings.json")
		if l, err := ompLevelFile(js, o.show(js)); err == nil {
			levels = append(levels, l)
		} else if !errors.Is(err, fs.ErrNotExist) {
			e.Warnings = append(e.Warnings, OmpSay{Key: "can't be read: {err}", Vars: map[string]string{"err": err.Error()}, File: o.show(js)})
		}
		config, _, _ := o.files(project)
		levels = append(levels, o.levelOf(*proj, config))
	}
	for _, l := range levels {
		for k, v := range l.settings {
			e.Settings[k] = OmpSourced{Value: v, From: l.from}
		}
		for k, v := range l.subagents {
			e.Subagents[k] = OmpSourced{Value: v, From: l.from}
		}
		if l.fallback != nil {
			e.Fallback = OmpSourcedList{Value: l.fallback, From: l.from}
		}
	}
	at := map[string]int{}
	for _, f := range o.found(project, "WATCHDOG.yml", "WATCHDOG.yaml") {
		shown := o.show(f)
		b, err := os.ReadFile(f)
		if err != nil {
			e.Warnings = append(e.Warnings, OmpSay{Key: "can't be read: {err}", Vars: map[string]string{"err": err.Error()}, File: shown})
			continue
		}
		w, warnings, err := ompWatchdogParse(b)
		if err != nil {
			s := err.(OmpSay)
			s.File = shown
			e.Warnings = append(e.Warnings, s)
			continue
		}
		for _, x := range warnings {
			x.File = shown
			e.Warnings = append(e.Warnings, x)
		}
		e.Rosters = append(e.Rosters, shown)
		if strings.TrimSpace(w.Instructions) != "" {
			e.Instructions = append(e.Instructions, OmpSourced{Value: strings.TrimSpace(w.Instructions), From: shown})
		}
		if w.MaxNotesPerUpdate >= 1 {
			e.MaxNotes = OmpSourced{Value: strconv.Itoa(w.MaxNotesPerUpdate), From: shown}
		}
		for _, a := range w.Advisors {
			// the tools it runs with: those omp has, its default for none
			a.Tools = ompToolsRun(a.Tools)
			if i, ok := at[a.slug()]; ok {
				e.Roster[i] = OmpEffectiveAdvisor{a, shown}
				continue
			}
			at[a.slug()] = len(e.Roster)
			e.Roster = append(e.Roster, OmpEffectiveAdvisor{a, shown})
		}
	}
	for _, f := range o.found(project, "WATCHDOG.md") {
		e.Notes = append(e.Notes, o.show(f))
	}
	return e
}

// subagents are the subagents task.agentAdvisor may name: omp's own, the
// .md files of the agents folders (the user's, the project's), and any the
// levels name already.
func (o *ompAdvisorAt) subagents(project string, levels ...OmpAdvisorLayer) []string {
	out := slices.Clone(ompBundledAgents)
	dirs := []string{filepath.Join(o.dir, "agents")}
	if project != "" {
		dirs = append(dirs, filepath.Join(project, ".omp", "agents"))
	}
	for _, d := range dirs {
		mds, _ := filepath.Glob(filepath.Join(d, "*.md"))
		for _, m := range mds {
			if n := ompAgentName(m); !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	for _, l := range levels {
		for n := range l.Subagents {
			if !slices.Contains(out, n) {
				out = append(out, n)
			}
		}
	}
	sort.Strings(out[len(ompBundledAgents):])
	return out
}

// ompAgentName is the name a subagent's .md gives itself in its
// frontmatter (name:), as omp's discovery takes it; its file's without
// one.
func ompAgentName(path string) string {
	stem := strings.TrimSuffix(filepath.Base(path), ".md")
	b, err := os.ReadFile(path)
	if err != nil {
		return stem
	}
	s := strings.ReplaceAll(strings.TrimPrefix(string(b), "\ufeff"), "\r\n", "\n")
	rest, ok := strings.CutPrefix(s, "---\n")
	if !ok {
		return stem
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return stem
	}
	var fm struct {
		Name string `yaml:"name"`
	}
	if yaml.Unmarshal([]byte(rest[:end]), &fm) == nil && strings.TrimSpace(fm.Name) != "" {
		return strings.TrimSpace(fm.Name)
	}
	return stem
}

// ompXDG: omp keeps its data under $XDG_DATA_HOME/omp, where that folder
// is there, off Windows (dirs.ts); a var for tests.
var ompXDG = runtime.GOOS != "windows"

// sessionRoots are where this row's omp keeps its sessions, for the
// projects it was run in: its agent folder's sessions, and with
// $XDG_DATA_HOME set the data folder's omp moves them to (agentSubdir
// "data"): omp/sessions for the default agent folder, omp/profiles/<name>
// for a profile's. An omp in WSL reads the distro's variables, not these.
func (o *ompAdvisorAt) sessionRoots() []string {
	roots := []string{filepath.Join(o.dir, "sessions")}
	xdg := appdir.Getenv("XDG_DATA_HOME")
	if !ompXDG || o.at.spell != nil || xdg == "" || filepath.Base(o.dir) != "agent" {
		return roots
	}
	data := filepath.Join(xdg, "omp")
	if up := filepath.Dir(o.dir); filepath.Base(filepath.Dir(up)) == "profiles" {
		data = filepath.Join(data, "profiles", filepath.Base(up))
	}
	return append(roots, filepath.Join(data, "sessions"))
}

// OmpAdvisorView is the advisor's settings of a's omp, with project's
// (as omp spells its folder; "" for none).
func (a *Agent) OmpAdvisorView(project string) (*OmpAdvisorView, error) {
	o := a.ompAdvisor
	if o == nil {
		return nil, ompSay("{agent} has no advisor settings", "agent", a.Name)
	}
	if o.down() {
		return nil, ompSay("{agent}'s distro isn't running: start it to see its advisor's settings", "agent", a.Name)
	}
	v := &OmpAdvisorView{Settings: OmpAdvisorSettings, Tools: OmpAdvisorTools, User: o.layer(""),
		Projects: sessions.OmpProjects(o.sessionRoots()...)}
	if v.Projects == nil {
		v.Projects = []sessions.OmpProject{}
	}
	v.Levels = map[string][]string{}
	if o.entry != nil {
		for _, m := range o.entry().Models {
			if m.Kind != "" { // an image model, which no advisor runs on
				continue
			}
			l := []string{}
			if m.Thinking != nil {
				l = append(l, m.Thinking.Efforts...)
			}
			v.Levels[magpieID+"/"+m.ID] = l
		}
	}
	local := ""
	if project != "" {
		var err error
		if local, err = o.project(project); err != nil {
			return nil, err
		}
		if !isDir(local) {
			return nil, ompSay("{dir} is not a folder", "dir", project)
		}
		l := o.layer(local)
		v.Project, v.ProjectDir = &l, project
		if strings.EqualFold(filepath.Clean(local), filepath.Clean(o.at.home)) {
			// omp reads no project config.yml in home (ll): ~/.omp is the
			// user's own
			v.ProjectHome = true
			l.Settings, l.Subagents, l.Fallback, l.ConfigError = map[string]string{}, map[string]string{}, nil, ""
			v.Project = &l
		}
	}
	levels := []OmpAdvisorLayer{v.User}
	if v.Project != nil {
		levels = append(levels, *v.Project)
	}
	v.Subagents = o.subagents(local, levels...)
	v.Effective = o.effective(local, v.User, v.Project)
	return v, nil
}

// OmpAdvisorApply makes ch at its level, and answers the view after it.
// Everything sent is checked before anything is written; a level whose
// files changed since the window read them (Version), or a file magpie
// can't read right (a config.yml that doesn't parse, a WATCHDOG.yml with
// what omp would pass over, a WATCHDOG.md it can't read) isn't written
// over. What it writes is recorded for Disconnect only while omp is
// connected: written otherwise, it is the user's own.
func (a *Agent) OmpAdvisorApply(ch OmpAdvisorChange) (*OmpAdvisorView, error) {
	o := a.ompAdvisor
	if o == nil {
		return nil, ompSay("{agent} has no advisor settings", "agent", a.Name)
	}
	if o.down() {
		return nil, ompSay("{agent}'s distro isn't running: start it to see its advisor's settings", "agent", a.Name)
	}
	if err := a.ompAdvisorApply(ch); err != nil {
		return nil, err
	}
	return a.OmpAdvisorView(ch.Project)
}

func (a *Agent) ompAdvisorApply(ch OmpAdvisorChange) error {
	o := a.ompAdvisor
	local := ""
	if ch.Project != "" {
		var err error
		if local, err = o.project(ch.Project); err != nil {
			return err
		}
		if !isDir(local) {
			return ompSay("{dir} is not a folder", "dir", ch.Project)
		}
	}
	config, watchdog, notes := o.files(local)
	var kvs []edit.KV
	var dels []string
	for k, v := range ch.Settings {
		s := ompAdvisorSetting(k)
		if s == nil {
			return ompSay("{key} is not one of omp's advisor settings", "key", k)
		}
		if v == "" {
			dels = append(dels, k)
			continue
		}
		x, err := s.value(v)
		if err != nil {
			return err
		}
		kvs = append(kvs, edit.KV{Path: k, Value: x})
	}
	for n, v := range ch.Subagents {
		if n == "" || strings.ContainsAny(n, ".\n") {
			return ompSay("{name} is not a subagent's name", "name", n)
		}
		if v = strings.TrimSpace(v); v == "" {
			dels = append(dels, "task.agentAdvisor."+n)
		} else {
			kvs = append(kvs, edit.KV{Path: "task.agentAdvisor." + n, Value: v})
		}
	}
	if ch.Fallback != nil {
		var chain []string
		for _, m := range *ch.Fallback {
			if m = strings.TrimSpace(m); m != "" {
				chain = append(chain, m)
			}
		}
		if len(chain) == 0 {
			dels = append(dels, "retry.fallbackChains.advisor")
		} else {
			kvs = append(kvs, edit.KV{Path: "retry.fallbackChains.advisor", Value: chain})
		}
	}
	keyed := len(kvs) > 0 || len(dels) > 0
	if keyed && local != "" && strings.EqualFold(filepath.Clean(local), filepath.Clean(o.at.home)) {
		return ompSay("omp reads no project config.yml in your home folder: set these for every project instead")
	}

	ompAdvisorMu.Lock()
	defer ompAdvisorMu.Unlock()
	if cur := o.layer(local).Version; ch.Version != cur {
		return OmpSay{Key: "The advisor's files changed since this window read them: close it and open it again", Stale: true}
	}
	if keyed {
		if err := ompYAMLOK(config); err != nil {
			return ompSay("{file} can't be read ({err}), so magpie doesn't write to it", "file", o.show(config), "err", err.Error())
		}
	}
	var text string
	if ch.Watchdog != nil {
		// a tool omp doesn't have is taken only where the file has it
		// already: the window keeps it as it was
		had := map[string]bool{}
		if b, err := os.ReadFile(watchdog); err == nil {
			w, warnings, err := ompWatchdogParse(b)
			if err == nil {
				warnings = slices.DeleteFunc(warnings, func(s OmpSay) bool { return s.Kept })
			}
			if err != nil || len(warnings) > 0 {
				return ompSay("{file} has what magpie can't keep: fix it by hand first", "file", o.show(watchdog))
			}
			for _, e := range w.Advisors {
				for _, t := range e.Tools {
					had[t] = true
				}
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return ompSay("{file} can't be read ({err}), so magpie doesn't write to it", "file", o.show(watchdog), "err", err.Error())
		}
		w := *ch.Watchdog
		w.Advisors = slices.Clone(w.Advisors)
		for i, e := range w.Advisors {
			nth := strconv.Itoa(i + 1)
			if bad := e.check(nth); bad != nil {
				return *bad
			}
			if e.Tools != nil {
				e.Tools = ompToolNames(e.Tools)
			}
			for _, t := range e.Tools {
				if !slices.Contains(OmpAdvisorTools, t) && !had[t] {
					return ompSay("advisor {n}: omp has no tool {tool} for an advisor", "n", nth, "tool", t)
				}
			}
			w.Advisors[i] = e
		}
		text = ompWatchdogText(w)
	}
	if ch.Notes != nil {
		if _, err := os.ReadFile(notes); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return ompSay("{file} can't be read ({err}), so magpie doesn't write to it", "file", o.show(notes), "err", err.Error())
		}
	}
	// recorded for Disconnect while omp is connected; a record magpie
	// can't read has nothing more written
	noting := a.Wired()
	if noting {
		if _, err := ompAdvisorRead(a); err != nil {
			return err
		}
	}
	rec := o.target(local)
	if local != "" {
		d := filepath.Join(local, ".omp")
		if !isDir(d) && (len(kvs) > 0 || text != "" || ch.Notes != nil && *ch.Notes != "") {
			if err := os.MkdirAll(d, 0o755); err != nil {
				return err
			}
			if err := ompAdvisorNote(a, noting, func(r *ompAdvisorRecord) { r.Dirs = append(r.Dirs, d) }); err != nil {
				return err
			}
		}
	}
	if keyed {
		var keys []string
		for _, kv := range kvs {
			keys = append(keys, kv.Path)
		}
		keys = append(keys, dels...)
		_, missing := os.Stat(config)
		if err := ompAdvisorNote(a, noting, func(r *ompAdvisorRecord) {
			if missing != nil {
				r.file(rec(config), ompWas{Config: true})
			}
			for _, k := range keys {
				t, ok := edit.GetYAMLText(config, k)
				r.key(rec(config), k, ompWas{Had: ok, Was: t})
			}
		}); err != nil {
			return err
		}
		if len(kvs) > 0 {
			if err := edit.SetYAML(config, kvs...); err != nil {
				return err
			}
		}
		if len(dels) > 0 {
			if err := edit.DelYAML(config, dels...); err != nil {
				return err
			}
		}
		if err := ompAdvisorNote(a, noting, func(r *ompAdvisorRecord) {
			for _, k := range keys {
				t, ok := edit.GetYAMLText(config, k)
				r.left(rec(config)+"\n"+k, t, !ok)
			}
		}); err != nil {
			return err
		}
	}
	if ch.Watchdog != nil {
		if err := ompWriteNoted(a, noting, watchdog, rec(watchdog), text); err != nil {
			return err
		}
	}
	if ch.Notes != nil {
		if err := ompWriteNoted(a, noting, notes, rec(notes), *ch.Notes); err != nil {
			return err
		}
	}
	return nil
}

// ompWriteNoted writes a whole file, text "" taking it away, recording
// what it held first and what magpie left. A file there that can't be
// read isn't written over.
func ompWriteNoted(a *Agent, noting bool, path, target, text string) error {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := ompAdvisorNote(a, noting, func(r *ompAdvisorRecord) { r.file(target, ompWas{Had: err == nil, Was: string(b)}) }); err != nil {
		return err
	}
	if text == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := edit.WriteAtomic(path, []byte(text)); err != nil {
			return err
		}
	}
	return ompAdvisorNote(a, noting, func(r *ompAdvisorRecord) { r.leftFile(target, text, text == "") })
}

// ompAdvisorRecord is what magpie wrote of the advisor's, kept in the stash
// (<agent>.advisor) for Disconnect to put back: each key of a config.yml
// and each whole file, by its target (agent:<name> for one in the agent
// folder, which a disconnect preview's copy has elsewhere; the full path
// for a project's), and the folders magpie made.
type ompAdvisorRecord struct {
	Keys  map[string]ompWas `json:"keys,omitempty"`
	Files map[string]ompWas `json:"files,omitempty"`
	Dirs  []string          `json:"dirs,omitempty"`
}

// ompWas is what a key or file held before magpie first wrote it (Had,
// Was) and what magpie left in it last (Now, Gone).
type ompWas struct {
	Had  bool   `json:"had,omitempty"`
	Was  string `json:"was,omitempty"`
	Now  string `json:"now,omitempty"`
	Gone bool   `json:"gone,omitempty"`
	// Config: a config.yml magpie made, whose keys are noted one by one
	Config bool `json:"config,omitempty"`
}

// unchanged says a key or file still holds what magpie left: none, or the
// same text.
func (w ompWas) unchanged(cur string, has bool) bool {
	if w.Gone {
		return !has
	}
	return has && cur == w.Now
}

// key notes what a key held before magpie's first write of it.
func (r *ompAdvisorRecord) key(file, k string, was ompWas) {
	if r.Keys == nil {
		r.Keys = map[string]ompWas{}
	}
	if _, ok := r.Keys[file+"\n"+k]; !ok {
		r.Keys[file+"\n"+k] = was
	}
}

func (r *ompAdvisorRecord) left(id, now string, gone bool) {
	w := r.Keys[id]
	w.Now, w.Gone = now, gone
	r.Keys[id] = w
}

// file notes what a file held before magpie's first write of it.
func (r *ompAdvisorRecord) file(target string, was ompWas) {
	if r.Files == nil {
		r.Files = map[string]ompWas{}
	}
	if _, ok := r.Files[target]; !ok {
		r.Files[target] = was
	}
}

func (r *ompAdvisorRecord) leftFile(target, now string, gone bool) {
	if w, ok := r.Files[target]; ok {
		w.Now, w.Gone = now, gone
		r.Files[target] = w
	}
}

// ompAdvisorKey is the stash key of an omp row's record.
func ompAdvisorKey(a *Agent) string { return a.ID + ".advisor" }

// empty: nothing is recorded.
func (r ompAdvisorRecord) empty() bool {
	return len(r.Keys) == 0 && len(r.Files) == 0 && len(r.Dirs) == 0
}

// ompAdvisorRead is a's record; an error for one that doesn't parse, which
// is unknown, not empty (code standards: a failed read means unknown).
func ompAdvisorRead(a *Agent) (ompAdvisorRecord, error) {
	var r ompAdvisorRecord
	raw := stashShared()[ompAdvisorKey(a)]
	if raw == "" {
		return r, nil
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return r, ompSay("magpie's record of what it wrote for the advisor can't be read ({err}), so it writes nothing more", "err", err.Error())
	}
	return r, nil
}

// ompAdvisorNoteHold runs between a note's read and its write; a var for
// tests to hold the two apart.
var ompAdvisorNoteHold = func() {}

// ompAdvisorNote changes the record of a's advisor writes, read, changed
// and written under stashMu as every change to the stash is; nothing when
// noting is false (omp isn't connected). A record emptied goes.
func ompAdvisorNote(a *Agent, noting bool, fn func(*ompAdvisorRecord)) error {
	if !noting {
		return nil
	}
	stashMu.Lock()
	defer stashMu.Unlock()
	m := stashLoad()
	var r ompAdvisorRecord
	if raw := m[ompAdvisorKey(a)]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return ompSay("magpie's record of what it wrote for the advisor can't be read ({err}), so it writes nothing more", "err", err.Error())
		}
	}
	fn(&r)
	ompAdvisorNoteHold()
	if r.empty() {
		delete(m, ompAdvisorKey(a))
	} else {
		b, _ := json.Marshal(r)
		m[ompAdvisorKey(a)] = string(b)
	}
	return stashWrite(m)
}

// target is how the record names a file: agent:<name> for one in the
// agent folder, its path for a project's.
func (o *ompAdvisorAt) target(project string) func(path string) string {
	return func(path string) string {
		if project == "" && filepath.Dir(path) == o.dir {
			return "agent:" + filepath.Base(path)
		}
		return path
	}
}

// local is where a target is now: the agent folder's in o's, a project's
// as it is; ok false for a project's when project is false.
func (o *ompAdvisorAt) local(target string, project bool) (string, bool) {
	if name, ok := strings.CutPrefix(target, "agent:"); ok {
		return filepath.Join(o.dir, name), true
	}
	return target, project
}

// ompAdvisorRestore puts back what magpie wrote of the advisor's, where it
// still reads as magpie left it; what the user changed since stays. What
// it can't look at (a file it can't read, a record that doesn't parse) is
// left as it is, and stays recorded. A disconnect's dry run (preview.go)
// puts back the agent folder's only: a project's files are outside its
// copy, and ompAdvisorBeyond shows them.
func (a *Agent) ompAdvisorRestore() error {
	o := a.ompAdvisor
	if o == nil || o.down() {
		return nil
	}
	ompAdvisorMu.Lock()
	defer ompAdvisorMu.Unlock()
	rec, err := ompAdvisorRead(a)
	if err != nil {
		log.Printf("omp's advisor isn't put back: %v", err)
		return nil
	}
	if rec.empty() {
		return nil
	}
	left, err := o.restore(rec, func(t string) (string, bool) { return o.local(t, !disconnectDryRun) })
	if err != nil {
		return err
	}
	return ompAdvisorNote(a, true, func(r *ompAdvisorRecord) { *r = left })
}

// restore puts back rec's keys and files at where says, the folders magpie
// made taken away when left empty. left is what it couldn't look at: a
// target where has none for, a file there that can't be read.
func (o *ompAdvisorAt) restore(rec ompAdvisorRecord, where func(target string) (string, bool)) (left ompAdvisorRecord, err error) {
	ids := make([]string, 0, len(rec.Keys))
	for id := range rec.Keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		w := rec.Keys[id]
		target, k, _ := strings.Cut(id, "\n")
		path, ok := where(target)
		if ok && ompYAMLOK(path) != nil {
			ok = false
		}
		if !ok {
			left.key(target, k, w)
			continue
		}
		cur, has := edit.GetYAMLText(path, k)
		if !w.unchanged(cur, has) {
			continue // changed since: the user's
		}
		switch {
		case w.Had:
			err = edit.SetYAML(path, edit.KV{Path: k, Value: edit.YAMLText(w.Was)})
		case has:
			err = edit.DelYAML(path, k)
			if err == nil {
				err = ompPrune(path, k)
			}
		}
		if err != nil {
			return left, err
		}
	}
	targets := make([]string, 0, len(rec.Files))
	for t := range rec.Files {
		targets = append(targets, t)
	}
	sort.Strings(targets)
	for _, t := range targets {
		w := rec.Files[t]
		path, ok := where(t)
		var b []byte
		var rerr error
		if ok {
			b, rerr = os.ReadFile(path)
		}
		if !ok || rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			left.file(t, w)
			continue
		}
		has := rerr == nil
		if w.Config {
			// a config.yml magpie made goes once nothing is left in it
			if !w.Had && has && ompEmptyYAML(b) {
				if err := os.Remove(path); err != nil {
					return left, err
				}
			}
			continue
		}
		if !w.unchanged(string(b), has) {
			continue
		}
		if w.Had {
			if err := edit.WriteAtomic(path, []byte(w.Was)); err != nil {
				return left, err
			}
		} else if has {
			if err := os.Remove(path); err != nil {
				return left, err
			}
		}
	}
	for _, d := range rec.Dirs {
		if path, ok := where(d); ok {
			os.Remove(path) // only while empty
		} else {
			left.Dirs = append(left.Dirs, d)
		}
	}
	return left, nil
}

// ompPrune takes out the mappings above key that its removal left empty
// (advisor: {}), which magpie made for it.
func ompPrune(path, key string) error {
	parts := strings.Split(key, ".")
	for i := len(parts) - 1; i > 0; i-- {
		parent := strings.Join(parts[:i], ".")
		if t, ok := edit.GetYAMLText(path, parent); !ok || t != "{}" {
			return nil
		}
		if err := edit.DelYAML(path, parent); err != nil {
			return err
		}
	}
	return nil
}

// ompEmptyYAML says a YAML file holds no setting: nothing, or mappings of
// nothing.
func ompEmptyYAML(b []byte) bool {
	var v any
	if yaml.Unmarshal(b, &v) != nil {
		return false
	}
	var empty func(any) bool
	empty = func(x any) bool {
		switch m := x.(type) {
		case nil:
			return true
		case map[string]any:
			for _, y := range m {
				if !empty(y) {
					return false
				}
			}
			return true
		}
		return false
	}
	return empty(v)
}

// ompAdvisorBeyond is how a disconnect changes the project files magpie
// wrote for the advisor, which the preview's copy of the agent folder
// doesn't hold: the restore run on copies of them.
func (a *Agent) ompAdvisorBeyond() []FileChange {
	o := a.ompAdvisor
	if o == nil || o.down() {
		return nil
	}
	ompAdvisorMu.Lock()
	defer ompAdvisorMu.Unlock()
	rec, err := ompAdvisorRead(a)
	if err != nil {
		return nil
	}
	var paths []string
	for id := range rec.Keys {
		t, _, _ := strings.Cut(id, "\n")
		if !strings.HasPrefix(t, "agent:") && !slices.Contains(paths, t) {
			paths = append(paths, t)
		}
	}
	for t := range rec.Files {
		if !strings.HasPrefix(t, "agent:") && !slices.Contains(paths, t) {
			paths = append(paths, t)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	sort.Strings(paths)
	tmp, err := os.MkdirTemp("", "magpie-preview-")
	if err != nil {
		return nil
	}
	defer removePreview(tmp)
	copyOf := map[string]string{}
	before := map[string][]byte{}
	for i, p := range paths {
		c := filepath.Join(tmp, strconv.Itoa(i), filepath.Base(p))
		os.MkdirAll(filepath.Dir(c), 0o700)
		if b, err := os.ReadFile(p); err == nil {
			before[p] = b
			if os.WriteFile(c, b, 0o600) != nil {
				return nil
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			continue // can't be read: a disconnect leaves it
		}
		copyOf[p] = c
	}
	if _, err := o.restore(ompAdvisorRecord{Keys: rec.Keys, Files: rec.Files}, func(t string) (string, bool) {
		c, ok := copyOf[t]
		return c, ok
	}); err != nil {
		return nil
	}
	var out []FileChange
	for _, p := range paths {
		was, had := before[p]
		now, err := os.ReadFile(copyOf[p])
		has := err == nil
		shown := o.show(p)
		switch {
		case had && !has:
			out = append(out, FileChange{Path: shown, Removed: true})
		case !had && has:
			out = append(out, FileChange{Path: shown, Created: true, Lines: diffLines("", string(now))})
		case had && has && !bytes.Equal(was, now):
			if l := diffLines(string(was), string(now)); len(l) > 0 {
				out = append(out, FileChange{Path: shown, Lines: l})
			}
		}
	}
	return out
}

// OmpAdvisorBrief is what the omp row says of its advisor: whether the
// switch is on, and the user's WATCHDOG.yml whose roster runs in the
// advisor role's place, with how many advisors it has. Row and Note are
// the words the GUI translates (the core's script budget): Row its
// Advisor row's, with {count} and {roster}, Note what the roles menu adds
// to the advisor role, "" for nothing.
type OmpAdvisorBrief struct {
	Enabled bool   `json:"enabled"`
	Roster  string `json:"roster,omitempty"`
	Count   int    `json:"count,omitempty"`
	Row     string `json:"row"`
	Note    string `json:"note,omitempty"`
}

// ompBriefParse is a WATCHDOG.yml's roster as the row's state reads it, once
// a change (filememo); a var for tests to count the parses.
var ompBriefParse = func(raw []byte) (OmpWatchdog, error) {
	w, _, err := ompWatchdogParse(raw)
	return w, err
}

// OmpAdvisorBrief is a's, nil for an agent that isn't omp or whose distro
// is stopped (down: asked again, as each of a WSL row's reads is). It
// reads config.yml through edit's memo and the WATCHDOG.yml beside it
// through filememo's, so a state poll parses neither again until it
// changes. A roster with the switch off runs nothing, as the role alone
// doesn't: the Note says so for either.
func (a *Agent) OmpAdvisorBrief() *OmpAdvisorBrief {
	o := a.ompAdvisor
	if o == nil || o.down() {
		return nil
	}
	v, _ := edit.GetYAML(o.config, "advisor.enabled")
	b := &OmpAdvisorBrief{Enabled: strings.EqualFold(v, "true")}
	names := map[string]bool{}
	for _, n := range []string{"WATCHDOG.yml", "WATCHDOG.yaml"} {
		w, err := filememo.Read("omp-watchdog", filepath.Join(o.dir, n), ompBriefParse)
		if err == nil && len(w.Advisors) > 0 {
			b.Roster = n
			for _, e := range w.Advisors {
				names[e.slug()] = true
			}
		}
	}
	b.Count = len(names)
	role, _ := edit.GetYAML(o.config, "modelRoles.advisor")
	switch {
	case b.Roster != "" && b.Enabled:
		b.Row, b.Note = "On · {roster}: {count} advisors", "taken over by WATCHDOG.yml"
		if b.Count == 1 {
			b.Row = "On · {roster}: 1 advisor"
		}
	case b.Roster != "":
		b.Row, b.Note = "Off · {roster}: {count} advisors", "advisor not enabled"
		if b.Count == 1 {
			b.Row = "Off · {roster}: 1 advisor"
		}
	case b.Enabled:
		b.Row = "On"
	default:
		b.Row = "Off"
		if role != "" {
			b.Note = "advisor not enabled"
		}
	}
	return b
}

// OmpAdvisorFolder is a folder the system's picker gave, as a's omp spells
// it: a distro's own path for one inside it, one of Windows' drives where
// the distro mounts it (its Drives, /mnt/<letter> by default).
func (a *Agent) OmpAdvisorFolder(local string) string {
	o := a.ompAdvisor
	if o == nil || o.at.spell == nil {
		return local
	}
	if o.at.posix != nil {
		return o.at.posix(local)
	}
	return o.at.native(local)
}

// rosters are the WATCHDOG.yml files whose advisors' models count as the
// config's do for magpie's provider (ompRefKeys): the agent folder's, and
// the projects' magpie wrote (its record). A project's the user wrote
// alone magpie can't know of.
func (o *ompAdvisorAt) rosters() []string {
	out := []string{filepath.Join(o.dir, "WATCHDOG.yml"), filepath.Join(o.dir, "WATCHDOG.yaml")}
	if o.agent == nil {
		return out
	}
	rec, _ := ompAdvisorRead(o.agent)
	var more []string
	for t := range rec.Files {
		if b := filepath.Base(t); !strings.HasPrefix(t, "agent:") && (b == "WATCHDOG.yml" || b == "WATCHDOG.yaml") {
			more = append(more, t)
		}
	}
	sort.Strings(more)
	return append(out, more...)
}

// rosterOnMagpie: an advisor of o's rosters is on one of magpie's models,
// which keeps magpie's provider in models.yml as a role on it does.
func (o *ompAdvisorAt) rosterOnMagpie() bool {
	for _, f := range o.rosters() {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		w, _, err := ompWatchdogParse(b)
		if err != nil {
			continue
		}
		for _, e := range w.Advisors {
			if usesMagpie(e.Model) {
				return true
			}
		}
	}
	return false
}

// ompRosterRename is a roster's text with move on each advisor's model,
// only those values changed in it (comments and layout kept); changed
// false for none, or text that doesn't parse.
func ompRosterRename(text string, move func(string) string) (string, bool) {
	var doc yaml.Node
	if yaml.Unmarshal([]byte(text), &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return text, false
	}
	type at struct {
		line, col int
		raw, now  string
	}
	var edits []at
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "advisors" || root.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, e := range root.Content[i+1].Content {
			for j := 0; e.Kind == yaml.MappingNode && j+1 < len(e.Content); j += 2 {
				v := e.Content[j+1]
				if e.Content[j].Value != "model" || !ompYAMLStr(v) {
					continue
				}
				if now := move(v.Value); now != v.Value {
					edits = append(edits, at{v.Line, v.Column, v.Value, now})
				}
			}
		}
	}
	if len(edits) == 0 {
		return text, false
	}
	lines := strings.SplitAfter(text, "\n")
	for _, e := range edits {
		if e.line < 1 || e.line > len(lines) {
			return text, false
		}
		l := lines[e.line-1]
		// the column, counted in characters
		start := 0
		for c := 1; c < e.col && start < len(l); c++ {
			_, size := utf8.DecodeRuneInString(l[start:])
			start += size
		}
		// the scalar as written: bare, or quoted (its quotes and escapes)
		end := start + len(e.raw)
		if start < len(l) && (l[start] == '"' || l[start] == '\'') {
			q := l[start]
			for end = start + 1; end < len(l); end++ {
				if q == '"' && l[end] == '\\' {
					end++
				} else if l[end] == q {
					if q == '\'' && end+1 < len(l) && l[end+1] == q {
						end++
						continue
					}
					break
				}
			}
			end++ // the closing quote
		} else if end > len(l) || l[start:end] != e.raw {
			return text, false
		}
		if end > len(l) || start >= end {
			return text, false
		}
		lines[e.line-1] = l[:start] + ompYAMLString(e.now) + l[end:]
	}
	out := strings.Join(lines, "")
	// what it reads back as must be the roster with the models moved
	before, _, err1 := ompWatchdogParse([]byte(text))
	after, _, err2 := ompWatchdogParse([]byte(out))
	if err1 != nil || err2 != nil || len(before.Advisors) != len(after.Advisors) {
		return text, false
	}
	for i := range before.Advisors {
		b := before.Advisors[i]
		b.Model = move(b.Model)
		if !reflect.DeepEqual(b, after.Advisors[i]) {
			return text, false
		}
	}
	return out, true
}

// renameRefs moves the models magpie's provider renamed (move) in what
// the advisor's record holds, as RenameRefs does the config's: each key
// magpie wrote in a config.yml (a project's too), the rosters (rosters),
// and the record's own texts of them, so a disconnect still knows them as
// magpie left them. moved: a file changed.
func (o *ompAdvisorAt) renameRefs(move func(string) string) (moved bool, err error) {
	if o.agent == nil {
		return false, nil
	}
	ompAdvisorMu.Lock()
	defer ompAdvisorMu.Unlock()
	rec, err := ompAdvisorRead(o.agent)
	if err != nil {
		return false, err
	}
	// each key: its text before and after, by record id
	before, after := map[string]string{}, map[string]string{}
	for id := range rec.Keys {
		t, k, _ := strings.Cut(id, "\n")
		path, _ := o.local(t, true)
		cur, ok := edit.GetYAMLText(path, k)
		if !ok {
			continue
		}
		before[id] = cur
		if err := edit.EditYAMLStrings(path, []string{k}, move); err != nil {
			return moved, err
		}
		now, _ := edit.GetYAMLText(path, k)
		after[id] = now
		moved = moved || now != cur
	}
	files := map[string][2]string{}
	for _, f := range o.rosters() {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		now, changed := ompRosterRename(string(b), move)
		if !changed {
			continue
		}
		if err := edit.WriteAtomic(f, []byte(now)); err != nil {
			return moved, err
		}
		moved = true
		files[o.target("")(f)] = [2]string{string(b), now}
	}
	return moved, ompAdvisorNote(o.agent, !rec.empty(), func(r *ompAdvisorRecord) {
		for id, w := range r.Keys {
			if cur, ok := before[id]; ok && !w.Gone && w.Now == cur {
				w.Now = after[id]
			}
			if w.Had {
				if t, err := edit.EditYAMLTextStrings(edit.YAMLText(w.Was), move); err == nil {
					w.Was = string(t)
				}
			}
			r.Keys[id] = w
		}
		for t, w := range r.Files {
			if f, ok := files[t]; ok && !w.Gone && w.Now == f[0] {
				w.Now = f[1]
			}
			if w.Had {
				w.Was, _ = ompRosterRename(w.Was, move)
			}
			r.Files[t] = w
		}
	})
}
