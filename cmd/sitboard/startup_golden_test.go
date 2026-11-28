package main

// Golden start-up blocks for TestStartBlockGolden, as printStart writes them
// for sampleInfo. Rewrite with: go test ./cmd/sitboard -run TestStartBlockGolden -update-startup

var startupGolden = map[string]string{
	"truecolor-dark": "" +
		"\x1b[38;2;245;197;66m▸\x1b[0m \x1b[1msitboard\x1b[0m \x1b[38;2;139;146;165m0.5.0\x1b[0m\x1b[38;2;139;146;165m (a122d90d)\x1b[0m\n" +
		"\n" +
		"\x1b[38;2;245;197;66mhttp://127.0.0.1:4317/?token=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\x1b[0m\n" +
		"\n" +
		"  claude code \x1b[38;2;143;199;154mfound\x1b[0m\x1b[38;2;139;146;165m 2.1.3\x1b[0m\n" +
		"  codex       \x1b[38;2;139;146;165mnot found\x1b[0m\n" +
		"  agterm      \x1b[38;2;236;124;124mfailing\x1b[0m\x1b[38;2;139;146;165m · agterm is not running\x1b[0m\n" +
		"  git         \x1b[38;2;143;199;154mfound\x1b[0m\x1b[38;2;139;146;165m 2.47.0, not a tested version\x1b[0m\n" +
		"  github      \x1b[38;2;139;146;165moff\x1b[0m\x1b[38;2;139;146;165m · --no-github\x1b[0m\n" +
		"  gitlab      \x1b[38;2;139;146;165masked once sitboard listens\x1b[0m\x1b[38;2;139;146;165m; the page shows what glab says\x1b[0m\n" +
		"  tmux        \x1b[38;2;139;146;165mchecked when you jump to a pane\x1b[0m\n" +
		"\n" +
		"  \x1b[38;2;139;146;165mstate file /srv/boards/board.json\x1b[0m\n" +
		"  \x1b[38;2;245;197;66mwarning:\x1b[0m could not lock the state file (its folder does not support locks); another sitboard on the same file would overwrite it\n" +
		"  \x1b[38;2;139;146;165mport 4317 is taken (another sitboard?), using 50123\x1b[0m\n" +
		"\n" +
		"\x1b[38;2;139;146;165mpress \x1b[0m\x1b[1mCtrl-C\x1b[0m\x1b[38;2;139;146;165m to stop\x1b[0m\n",
	"truecolor-light": "" +
		"\x1b[38;2;154;106;0m▸\x1b[0m \x1b[1msitboard\x1b[0m \x1b[38;2;95;101;120m0.5.0\x1b[0m\x1b[38;2;95;101;120m (a122d90d)\x1b[0m\n" +
		"\n" +
		"\x1b[38;2;154;106;0mhttp://127.0.0.1:4317/?token=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\x1b[0m\n" +
		"\n" +
		"  claude code \x1b[38;2;47;122;62mfound\x1b[0m\x1b[38;2;95;101;120m 2.1.3\x1b[0m\n" +
		"  codex       \x1b[38;2;95;101;120mnot found\x1b[0m\n" +
		"  agterm      \x1b[38;2;176;48;48mfailing\x1b[0m\x1b[38;2;95;101;120m · agterm is not running\x1b[0m\n" +
		"  git         \x1b[38;2;47;122;62mfound\x1b[0m\x1b[38;2;95;101;120m 2.47.0, not a tested version\x1b[0m\n" +
		"  github      \x1b[38;2;95;101;120moff\x1b[0m\x1b[38;2;95;101;120m · --no-github\x1b[0m\n" +
		"  gitlab      \x1b[38;2;95;101;120masked once sitboard listens\x1b[0m\x1b[38;2;95;101;120m; the page shows what glab says\x1b[0m\n" +
		"  tmux        \x1b[38;2;95;101;120mchecked when you jump to a pane\x1b[0m\n" +
		"\n" +
		"  \x1b[38;2;95;101;120mstate file /srv/boards/board.json\x1b[0m\n" +
		"  \x1b[38;2;154;106;0mwarning:\x1b[0m could not lock the state file (its folder does not support locks); another sitboard on the same file would overwrite it\n" +
		"  \x1b[38;2;95;101;120mport 4317 is taken (another sitboard?), using 50123\x1b[0m\n" +
		"\n" +
		"\x1b[38;2;95;101;120mpress \x1b[0m\x1b[1mCtrl-C\x1b[0m\x1b[38;2;95;101;120m to stop\x1b[0m\n",
	"256-dark": "" +
		"\x1b[38;5;221m▸\x1b[0m \x1b[1msitboard\x1b[0m \x1b[38;5;103m0.5.0\x1b[0m\x1b[38;5;103m (a122d90d)\x1b[0m\n" +
		"\n" +
		"\x1b[38;5;221mhttp://127.0.0.1:4317/?token=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\x1b[0m\n" +
		"\n" +
		"  claude code \x1b[38;5;114mfound\x1b[0m\x1b[38;5;103m 2.1.3\x1b[0m\n" +
		"  codex       \x1b[38;5;103mnot found\x1b[0m\n" +
		"  agterm      \x1b[38;5;210mfailing\x1b[0m\x1b[38;5;103m · agterm is not running\x1b[0m\n" +
		"  git         \x1b[38;5;114mfound\x1b[0m\x1b[38;5;103m 2.47.0, not a tested version\x1b[0m\n" +
		"  github      \x1b[38;5;103moff\x1b[0m\x1b[38;5;103m · --no-github\x1b[0m\n" +
		"  gitlab      \x1b[38;5;103masked once sitboard listens\x1b[0m\x1b[38;5;103m; the page shows what glab says\x1b[0m\n" +
		"  tmux        \x1b[38;5;103mchecked when you jump to a pane\x1b[0m\n" +
		"\n" +
		"  \x1b[38;5;103mstate file /srv/boards/board.json\x1b[0m\n" +
		"  \x1b[38;5;221mwarning:\x1b[0m could not lock the state file (its folder does not support locks); another sitboard on the same file would overwrite it\n" +
		"  \x1b[38;5;103mport 4317 is taken (another sitboard?), using 50123\x1b[0m\n" +
		"\n" +
		"\x1b[38;5;103mpress \x1b[0m\x1b[1mCtrl-C\x1b[0m\x1b[38;5;103m to stop\x1b[0m\n",
	"256-light": "" +
		"\x1b[38;5;136m▸\x1b[0m \x1b[1msitboard\x1b[0m \x1b[38;5;60m0.5.0\x1b[0m\x1b[38;5;60m (a122d90d)\x1b[0m\n" +
		"\n" +
		"\x1b[38;5;136mhttp://127.0.0.1:4317/?token=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\x1b[0m\n" +
		"\n" +
		"  claude code \x1b[38;5;28mfound\x1b[0m\x1b[38;5;60m 2.1.3\x1b[0m\n" +
		"  codex       \x1b[38;5;60mnot found\x1b[0m\n" +
		"  agterm      \x1b[38;5;124mfailing\x1b[0m\x1b[38;5;60m · agterm is not running\x1b[0m\n" +
		"  git         \x1b[38;5;28mfound\x1b[0m\x1b[38;5;60m 2.47.0, not a tested version\x1b[0m\n" +
		"  github      \x1b[38;5;60moff\x1b[0m\x1b[38;5;60m · --no-github\x1b[0m\n" +
		"  gitlab      \x1b[38;5;60masked once sitboard listens\x1b[0m\x1b[38;5;60m; the page shows what glab says\x1b[0m\n" +
		"  tmux        \x1b[38;5;60mchecked when you jump to a pane\x1b[0m\n" +
		"\n" +
		"  \x1b[38;5;60mstate file /srv/boards/board.json\x1b[0m\n" +
		"  \x1b[38;5;136mwarning:\x1b[0m could not lock the state file (its folder does not support locks); another sitboard on the same file would overwrite it\n" +
		"  \x1b[38;5;60mport 4317 is taken (another sitboard?), using 50123\x1b[0m\n" +
		"\n" +
		"\x1b[38;5;60mpress \x1b[0m\x1b[1mCtrl-C\x1b[0m\x1b[38;5;60m to stop\x1b[0m\n",
}
