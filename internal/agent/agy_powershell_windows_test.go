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
// for it, read nothing, and started agy without the gateway's key. Here
// a GUI-subsystem stand-in for magpie answers agy env, a console
// stand-in for agy prints what it was started with, and the function is
// run in each PowerShell on this machine.
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
	if len(os.Args) == 3 && os.Args[1] == "agy" && os.Args[2] == "env" {
		time.Sleep(300 * time.Millisecond) // the app reads its settings first
		fmt.Println("GEMINI_API_KEY=k-agy")
		fmt.Println("GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:3425")
		fmt.Println("model=magpie/p/m")
		fmt.Println("settings=C:\\agy\\settings.json")
	}
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
	fn := agyFunction("powershell", filepath.Join(bin, "magpie.exe"))
	want := `key=k-agy url=http://127.0.0.1:3425 args=--settings C:\agy\settings.json --model magpie/p/m`
	for _, sh := range shells {
		cmd := proc.Command(sh, "-NoProfile", "-NonInteractive", "-Command", fn+"\nagy")
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GEMINI_API_KEY=", "GOOGLE_GEMINI_BASE_URL=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", filepath.Base(sh), err, out)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Errorf("%s started agy as\n%s\nwant\n%s", filepath.Base(sh), got, want)
		}
	}
}
