# jdebug — build/test conveniences. The bash kit needs no build; the Go TUI
# is the interactive frontend (a dev build via `make tui`, or the vendored,
# hash-verified binaries under vendor/tui/ kept fresh by the git hooks).

.PHONY: tui vendor-tui hooks test clean

tui:            ## build the Go TUI for THIS machine (jdebug prefers tui/jdebug-tui)
	cd tui && go build -o jdebug-tui .

vendor-tui:     ## (re)build + hash the committed multi-platform TUI binaries
	scripts/vendor-tui.sh

hooks:          ## install the git hooks (pre-commit vendors the TUI, pre-push proves it)
	git config core.hooksPath githooks
	@echo "hooks installed: commits keep vendor/tui fresh; pushes verify its hashes"

test:           ## full suite: bash kit + Go frontend (Go parts need a toolchain)
	tests/run-tests.sh

clean:
	rm -f tui/jdebug-tui
