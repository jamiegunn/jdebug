package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config is the resolved runtime configuration, with v1's exact precedence:
// flags > environment > saved target file > built-ins. The saved file is
// PARSED, never executed — the v1 audit's config-sourcing hazard class does
// not exist here by construction.
type Config struct {
	KitRoot      string // the jdebug checkout (env JDEBUG_KIT, set by the dispatcher)
	DumpsRoot    string // $JDEBUG_DUMPS, default <kit>/dumps
	OutDir       string // $OUT_DIR — explicit per-run session dir override
	ActuatorBase string // $ACTUATOR_BASE / --actuator-base
	ActuatorAuth string // "bearer:VAR" | "basic:UVAR:PVAR" — pod env NAMES, never secrets
	Target       Target
	Quiet        bool // JDEBUG_QUIET silences the target announcement
}

// LoadConfig resolves configuration from the environment and the saved
// target file. Flag values are applied by the caller afterwards (flags win).
func LoadConfig() Config {
	kit := os.Getenv("JDEBUG_KIT")
	if kit == "" {
		if exe, err := os.Executable(); err == nil {
			// core/jdebug-core lives one level under the kit
			kit = filepath.Dir(filepath.Dir(exe))
		}
	}
	saved := loadSavedTarget()
	get := func(env, savedKey, def string) string {
		if v, ok := os.LookupEnv(env); ok {
			return v
		}
		if v, ok := saved[savedKey]; ok {
			return v
		}
		return def
	}
	cfg := Config{
		KitRoot: kit,
		Quiet:   os.Getenv("JDEBUG_QUIET") != "",
		Target: Target{
			Namespace: get("JDEBUG_NAMESPACE", "SAVED_NAMESPACE", "default"),
			Selector:  get("JDEBUG_SELECTOR", "SAVED_SELECTOR", ""),
			Container: get("JDEBUG_CONTAINER", "SAVED_CONTAINER", "app"),
		},
		ActuatorBase: firstNonEmpty(os.Getenv("ACTUATOR_BASE"), saved["SAVED_ACTUATOR"], "http://localhost:8080/actuator"),
		ActuatorAuth: get("JDEBUG_ACTUATOR_AUTH", "SAVED_ACTUATOR_AUTH", os.Getenv("ACTUATOR_AUTH")),
	}
	if cfg.ActuatorAuth == "" {
		cfg.ActuatorAuth = os.Getenv("ACTUATOR_AUTH")
	}
	cfg.DumpsRoot = firstNonEmpty(os.Getenv("JDEBUG_DUMPS"), filepath.Join(kit, "dumps"))
	cfg.OutDir = os.Getenv("OUT_DIR")
	return cfg
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// Announce prints the resolved target once, exactly like v1's
// announce_target — every command makes clear which pod it will hit.
func (c Config) Announce() {
	if c.Quiet || os.Getenv("JDEBUG_TARGET_ANNOUNCED") != "" {
		return
	}
	os.Setenv("JDEBUG_TARGET_ANNOUNCED", "1")
	sel := c.Target.Selector
	if sel == "" {
		sel = "<any pod>"
	}
	fmt.Fprintf(os.Stderr, "jdebug → namespace=%s  selector=%s  container=%s\n",
		c.Target.Namespace, sel, c.Target.Container)
}

// loadSavedTarget reads the remembered-target file written by the TUI's
// editor (printf %q assignments). It is a PARSER: lines that aren't plain
// SAVED_* assignments — or any command-substitution token — make the whole
// file ignored (with a warning), never evaluated.
func loadSavedTarget() map[string]string {
	dir := os.Getenv("JDEBUG_CONFIG_DIR")
	if dir == "" {
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			home, _ := os.UserHomeDir()
			xdg = filepath.Join(home, ".config")
		}
		dir = filepath.Join(xdg, "jdebug")
	}
	b, err := os.ReadFile(filepath.Join(dir, "target"))
	if err != nil {
		return nil
	}
	content := string(b)
	if strings.Contains(content, "$(") || strings.Contains(content, "`") {
		fmt.Fprintf(os.Stderr, "warning: ignoring %s — unexpected content (not a saved-target file); using defaults\n",
			filepath.Join(dir, "target"))
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 || !isSavedKey(line[:eq]) {
			fmt.Fprintf(os.Stderr, "warning: ignoring %s — unexpected content (not a saved-target file); using defaults\n",
				filepath.Join(dir, "target"))
			return nil
		}
		out[line[:eq]] = unquoteBash(line[eq+1:])
	}
	return out
}

func isSavedKey(k string) bool {
	if !strings.HasPrefix(k, "SAVED_") {
		return false
	}
	for _, r := range k {
		if (r < 'A' || r > 'Z') && r != '_' {
			return false
		}
	}
	return true
}

// unquoteBash undoes printf %q for the value shapes the editor writes:
// plain words, backslash-escaped strings, ”-quoted, and $'...' ANSI-C.
func unquoteBash(v string) string {
	switch {
	case strings.HasPrefix(v, "$'") && strings.HasSuffix(v, "'") && len(v) >= 3:
		body := v[2 : len(v)-1]
		var sb strings.Builder
		for i := 0; i < len(body); i++ {
			if body[i] == '\\' && i+1 < len(body) {
				i++
				switch body[i] {
				case 'n':
					sb.WriteByte('\n')
				case 't':
					sb.WriteByte('\t')
				case 'r':
					sb.WriteByte('\r')
				default:
					sb.WriteByte(body[i])
				}
				continue
			}
			sb.WriteByte(body[i])
		}
		return sb.String()
	case strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2:
		return strings.ReplaceAll(v[1:len(v)-1], `'\''`, "'")
	default:
		var sb strings.Builder
		for i := 0; i < len(v); i++ {
			if v[i] == '\\' && i+1 < len(v) {
				i++
			}
			sb.WriteByte(v[i])
		}
		return sb.String()
	}
}
