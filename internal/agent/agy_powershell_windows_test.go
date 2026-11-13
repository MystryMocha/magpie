package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/proc"
)

// PowerShell's function agy reads `magpie agy env` from the Windows app,
// a GUI program (#6084). Run outside a pipeline, PowerShell didn't wait
// for it, read nothing, and started agy without the gateway's key and
// without a word. Here a GUI-subsystem stand-in for magpie answers agy
// env, a console stand-in for agy prints what it was started with, and
// both functions, the built-in's and agent-agy's (launchSpec), are run in
// each PowerShell on this machine: on magpie, off it (env prints
// nothing), and with env failing, which is told.
func TestAgyPowerShellReadsGUIMagpie(t *testing.T) {
	var shells []string
	for _, n := range []string{"pwsh.exe", "powershell.exe"} {
		if p := proc.FindTool(n); p != "" {
			shells = append(shells, p)
		}
	}
	if len(shells) == 0 {
		t.Skip("no PowerShell")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	build := func(name, gui, src string) {
		t.Helper()
		mod := filepath.Join(dir, name+"-src")
		if err := os.MkdirAll(mod, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, text := range map[string]string{
			"go.mod":  "module " + name + "\n\ngo 1.26\n",
			"main.go": src,
		} {
			if err := os.WriteFile(filepath.Join(mod, f), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"build", "-o", filepath.Join(bin, name+".exe")}
		if gui != "" {
			args = append(args, "-ldflags", gui)
		}
		cmd := proc.Command("go", append(args, ".")...)
		cmd.Dir = mod
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOTOOLCHAIN=local", "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, out)
		}
	}
	build("magpie", "-H=windowsgui", `package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "agy" || os.Args[2] != "env" {
		os.Exit(2)
	}
	time.Sleep(300 * time.Millisecond) // the app reads its settings first
	switch os.Getenv("STUB_AGY") {
	case "off":
		return
	case "fail":
		os.Exit(3)
	}
	fmt.Println("GEMINI_API_KEY=k-agy")
	fmt.Println("GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425")
	fmt.Println("settings=C:\\agy\\settings.json")
	fmt.Println("model=magpie/p/m")
}
`)
	build("agy", "", `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	fmt.Printf("key=%s url=%s args=%s\n", os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_GEMINI_BASE_URL"), strings.Join(os.Args[1:], " "))
}
`)
	prog := filepath.Join(bin, "magpie.exe")
	on := `args=--settings C:\agy\settings.json --model magpie/p/m -p hi`
	plain := "key= url= args=-p hi"
	for _, fn := range []struct{ name, text, on string }{
		{"built-in", agyFunction("powershell", prog), "key=k-agy url=http://127.0.0.1:3425 " + on},
		// agent-agy's launch names GEMINI_API_KEY alone.
		{"agent-agy", newLaunch(agyLaunchDesc(), `C:\agy\settings.json`).function("powershell", prog), "key=k-agy url= " + on},
	} {
		for _, sh := range shells {
			for _, c := range []struct{ stub, want, warn string }{
				{"", fn.on, ""},
				{"off", plain, ""},
				{"fail", plain, "magpie agy env failed (exit 3), so agy starts without magpie"},
			} {
				cmd := proc.Command(sh, "-NoProfile", "-NonInteractive", "-Command", fn.text+"\nagy -p hi")
				cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
					"GEMINI_API_KEY=", "GOOGLE_GEMINI_BASE_URL=", "STUB_AGY="+c.stub)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s %s %q: %v\n%s", fn.name, filepath.Base(sh), c.stub, err, out)
				}
				// A warning's prefix is in the system's language (警告:)
				// and pwsh colours it, so it is known by its text.
				var lines []string
				warned := false
				for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
					l = strings.TrimSpace(l)
					if strings.Contains(l, " env failed ") {
						warned = c.warn != "" && strings.Contains(l, c.warn)
						if c.warn == "" {
							t.Errorf("%s %s %q warned: %s", fn.name, filepath.Base(sh), c.stub, l)
						}
						continue
					}
					lines = append(lines, l)
				}
				if got := strings.Join(lines, "\n"); got != c.want {
					t.Errorf("%s %s %q started agy as\n%s\nwant\n%s", fn.name, filepath.Base(sh), c.stub, got, c.want)
				}
				if c.warn != "" && !warned {
					t.Errorf("%s %s %q: no warning %q in\n%s", fn.name, filepath.Base(sh), c.stub, c.warn, out)
				}
			}
		}
	}
}
