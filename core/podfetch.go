package core

import "fmt"

// In-pod HTTP snippets — the Go port of v1's pod_fetch/_pod_auth
// (lib/common.sh). The pod's own HTTP client does the fetch: curl when
// present, else busybox wget (a stock JRE-alpine ships wget, not curl).
// Auth specs reference env var NAMES that live INSIDE the pod
// ("bearer:VAR" / "basic:UVAR:PVAR"); the emitted flags contain a literal
// $VAR for the pod's shell to expand — the secret never leaves the pod.

func podAuthFlags(client, spec string) string {
	switch {
	case len(spec) > 7 && spec[:7] == "bearer:":
		v := spec[7:]
		if client == "curl" {
			return fmt.Sprintf(`-H "Authorization: Bearer $%s" `, v)
		}
		return fmt.Sprintf(`--header="Authorization: Bearer $%s" `, v)
	case len(spec) > 6 && spec[:6] == "basic:":
		rest := spec[6:]
		for i := 0; i < len(rest); i++ {
			if rest[i] == ':' {
				u, p := rest[:i], rest[i+1:]
				if u == "" || p == "" {
					return ""
				}
				if client == "curl" {
					return fmt.Sprintf(`-u "$%s:$%s" `, u, p)
				}
				return fmt.Sprintf(`--user="$%s" --password="$%s" `, u, p)
			}
		}
	}
	return ""
}

// PodFetchScript emits the sh snippet that GETs url from inside the pod.
// Run via ExecPod(..., "sh", "-c", script).
func PodFetchScript(url, accept, auth string) string {
	ac := podAuthFlags("curl", auth)
	aw := podAuthFlags("wget", auth)
	nohttp := `echo 'error: neither curl nor wget exists in this container — the actuator tier cannot run here (jattach needs no HTTP: --via jattach)' >&2; exit 127`
	if accept != "" {
		return fmt.Sprintf(`if command -v curl >/dev/null 2>&1; then curl -fsS %s-H 'Accept: %s' '%s'; elif command -v wget >/dev/null 2>&1; then wget -qO- %s--header='Accept: %s' '%s' 2>/dev/null || wget -qO- '%s'; else %s; fi`,
			ac, accept, url, aw, accept, url, url, nohttp)
	}
	return fmt.Sprintf(`if command -v curl >/dev/null 2>&1; then curl -fsS %s'%s'; elif command -v wget >/dev/null 2>&1; then wget -qO- %s'%s'; else %s; fi`,
		ac, url, aw, url, nohttp)
}

// PodHTTPStatusScript emits an sh snippet printing ONLY the HTTP status for
// url (000 when undeterminable) — how a failed fetch is classified as
// secured (401/403) vs absent (404) vs wedged (000).
func PodHTTPStatusScript(url, auth string) string {
	ac := podAuthFlags("curl", auth)
	return fmt.Sprintf(`if command -v curl >/dev/null 2>&1; then curl -s -o /dev/null -w '%%{http_code}' %s'%s' 2>/dev/null || echo 000; elif command -v wget >/dev/null 2>&1; then wget -S -O /dev/null '%s' 2>&1 | awk '/HTTP\/[0-9]/{c=$2} END{print (c==""?"000":c)}'; else echo 000; fi`,
		ac, url, url)
}
