package gui

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/agent"
)

// omp's Advisor (#5019) on a named profile: the dialog's Save, sent as
// ompadvisor.js sends it, writes the profile's config.yml, WATCHDOG.yml and
// WATCHDOG.md and nothing of the default omp's; the state's rows then say
// the profile's switch is on and its roster has three advisors, and the
// default omp's that its advisor role has a model but isn't enabled. A
// save sends the version GET read: one made on a version since changed is
// a 409, and what the agent says goes as say for the window to translate.
// A field the API doesn't know is refused.
func TestOmpAdvisorAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("PI_CONFIG_DIR", "")
	t.Setenv("OMP_PROFILE", "")
	def := filepath.Join(home, ".omp", "agent", "config.yml")
	os.MkdirAll(filepath.Dir(def), 0o755)
	os.WriteFile(def, []byte("modelRoles:\n  default: own/model-a\n"), 0o600)
	work := filepath.Join(home, ".omp", "profiles", "work", "agent")
	os.MkdirAll(work, 0o755)
	os.WriteFile(filepath.Join(work, "config.yml"), []byte("# mine\nmodelRoles:\n  default: own/model-b\n"), 0o600)
	h := Handler(nil, nil)
	call := func(method, path, body string) (int, []byte) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec.Code, rec.Body.Bytes()
	}
	code, b := call("GET", "/api/agents/omp-advisor/omp%23work", "")
	var v agent.OmpAdvisorView
	if code != 200 || json.Unmarshal(b, &v) != nil {
		t.Fatalf("GET %d %s", code, b)
	}
	if len(v.Settings) != 8 || v.User.Config != "~/.omp/profiles/work/agent/config.yml" {
		t.Fatalf("view: %d settings, config %q", len(v.Settings), v.User.Config)
	}
	before, _ := os.ReadFile(def)

	// what the dialog posted in its preview (enabled on, a subagent off, a
	// fallback, the roster with a third advisor added, the notes)
	save := `{"settings":{"advisor.enabled":"true"},"subagents":{"reviewer":"off"},"fallback":["magpie/deepseek/flash"],` +
		`"watchdog":{"instructions":"Keep the diff small.","advisors":[{"name":"Quick","model":"magpie/deepseek/flash:high","tools":null,"reviewInterval":3},` +
		`{"name":"final","tools":["read","grep"],"reviewMode":"agent-end","instructions":"Check the tests ran.\nSay what is missing."},{"name":"advisor-3","tools":null}]},` +
		`"notes":"Watch for secrets in logs.\nAnd the tests.","version":"` + v.User.Version + `"}`
	if code, b := call("POST", "/api/agents/omp-advisor/omp%23work", save); code != 200 {
		t.Fatalf("POST %d %s", code, b)
	}
	// the same save again: the files changed since that version was read,
	// so it is refused as a conflict, in words the window translates
	code, b = call("POST", "/api/agents/omp-advisor/omp%23work", save)
	var said struct {
		Error string
		Say   agent.OmpSay
	}
	if json.Unmarshal(b, &said); code != 409 || said.Say.Key != "The advisor's files changed since this window read them: close it and open it again" || said.Error == "" {
		t.Fatalf("a stale save: %d %s", code, b)
	}
	// a value omp doesn't take: 400, with its words and their slots
	_, b = call("GET", "/api/agents/omp-advisor/omp%23work", "")
	json.Unmarshal(b, &v)
	code, b = call("POST", "/api/agents/omp-advisor/omp%23work", `{"settings":{"advisor.enabled":"yes"},"version":"`+v.User.Version+`"}`)
	said.Say = agent.OmpSay{}
	if json.Unmarshal(b, &said); code != 400 || said.Say.Key != "{value} is not a value omp takes for {key}" || said.Say.Vars["value"] != "yes" {
		t.Fatalf("a bad value: %d %s", code, b)
	}
	cfg, _ := os.ReadFile(filepath.Join(work, "config.yml"))
	for _, want := range []string{"# mine", "enabled: true", "reviewer: \"off\"", "magpie/deepseek/flash"} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("config.yml lacks %q:\n%s", want, cfg)
		}
	}
	wd, _ := os.ReadFile(filepath.Join(work, "WATCHDOG.yml"))
	for _, want := range []string{"instructions: Keep the diff small.", "  - name: Quick\n    model: magpie/deepseek/flash:high\n", "      - read\n      - grep\n", "  - name: advisor-3"} {
		if !strings.Contains(string(wd), want) {
			t.Fatalf("WATCHDOG.yml lacks %q:\n%s", want, wd)
		}
	}
	if md, _ := os.ReadFile(filepath.Join(work, "WATCHDOG.md")); string(md) != "Watch for secrets in logs.\nAnd the tests." {
		t.Fatalf("WATCHDOG.md %q", md)
	}
	if after, _ := os.ReadFile(def); string(after) != string(before) {
		t.Fatalf("the default omp's config.yml changed:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(home, ".omp", "agent", "WATCHDOG.yml")); !os.IsNotExist(err) {
		t.Fatalf("the default omp got a WATCHDOG.yml: %v", err)
	}

	_, b = call("GET", "/api/state", "")
	var s stateJSON
	json.Unmarshal(b, &s)
	got := map[string]*agent.OmpAdvisorBrief{}
	for _, a := range s.Agents {
		got[a.ID] = a.Advisor
	}
	if w := got["omp#work"]; w == nil || !w.Enabled || w.Roster != "WATCHDOG.yml" || w.Count != 3 || w.Row != "On · {roster}: {count} advisors" || w.Note != "taken over by WATCHDOG.yml" {
		t.Fatalf("omp#work's advisor %+v", w)
	}
	if d := got["omp"]; d == nil || d.Enabled || d.Roster != "" || d.Row != "Off" || d.Note != "" {
		t.Fatalf("omp's advisor %+v", d)
	}
	// a model in the advisor role with the switch off runs nothing: said
	os.WriteFile(def, []byte("modelRoles:\n  default: own/model-a\n  advisor: own/model-a\n"), 0o600)
	_, b = call("GET", "/api/state", "")
	s = stateJSON{}
	json.Unmarshal(b, &s)
	for _, a := range s.Agents {
		if a.ID == "omp" && (a.Advisor == nil || a.Advisor.Note != "advisor not enabled") {
			t.Fatalf("omp's advisor with a role and no switch %+v", a.Advisor)
		}
	}
	if code, _ := call("POST", "/api/agents/omp-advisor/omp%23work", `{"bogus":1}`); code == 200 {
		t.Fatal("an unknown field was taken")
	}
}
