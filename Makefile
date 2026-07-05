# jdebug — build/test conveniences. The bash kit needs no build; this is for
# the optional Go TUI frontend.

.PHONY: tui test clean

tui:            ## build the Go TUI frontend (jdebug then prefers it)
	cd tui && go build -o jdebug-tui .

test:           ## full suite: bash kit + Go frontend
	tests/run-tests.sh

clean:
	rm -f tui/jdebug-tui
