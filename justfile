# Must match state_root in the host's config.toml. Only status/logs on non-systemd
# (loop) hosts read it; systemd hosts use journald.
runlog := "~/.local/state/flocc/run.log"

[private]
default:
    @just --list --unsorted

# Format Python and Go sources in place
[group('dev')]
fmt:
    uv run ruff check --select I --fix
    uv run ruff format
    gofmt -w deploy installer

# Lint Python, the setup script and Go without changing files
[group('dev')]
lint:
    uv run ruff format --check
    uv run ruff check
    uv run pyrefly check
    shellcheck setup
    test -z "$(gofmt -l deploy installer)"
    go vet ./...

# Run the Python and Go test suites
[group('dev')]
test: test-py test-go

# Run the Python tests
[group('dev')]
test-py:
    uv run pytest -q

# Run the Go tests
[group('dev')]
test-go:
    go test ./...

# Lint, then test; what CI and pre-merge run
[group('dev')]
check: lint test

# Build flocc-setup for this machine and for linux amd64/arm64 hosts into build/
[group('build')]
build-setup:
    mkdir -p build
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o build/flocc-setup-linux-amd64 ./installer
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o build/flocc-setup-linux-arm64 ./installer
    go build -o build/flocc-setup ./installer

[private]
build-desktop:
    mkdir -p build
    go build -o build/flocc-setup ./installer

# Remove build/
[group('build')]
clean:
    rm -rf build

# Set up or upgrade a host without a terminal (--answers FILE sends the answers), or --interactive
[group('host')]
ship target *flags: build-setup
    build/flocc-setup ship {{flags}} {{target}}

# Log the host in to Claude, accept Remote Control and start its timer (--email picks the account)
[group('host')]
login target *flags: build-desktop
    build/flocc-setup login {{flags}} {{target}}

# Run any command on the host as flocc, with the service PATH
[group('host')]
[positional-arguments]
host target *cmd: build-desktop
    build/flocc-setup host "$@"

# Run flocc doctor on the host
[group('host')]
doctor target: build-desktop
    build/flocc-setup host {{target}} flocc doctor

# Show tmux sessions, the last run.log lines and the timer on the host
[group('host')]
status target: build-desktop
    build/flocc-setup host {{target}} 'tmux -L flocc ls; tail -n 10 {{runlog}}; [ -d /run/systemd/system ] && XDG_RUNTIME_DIR=/run/user/$(id -u) systemctl --user list-timers flocc.timer; true'

# Show every host's loop, sessions, usage and disk in one table
[group('host')]
fleet-status *flags: build-desktop
    build/flocc-setup fleet {{flags}}

# Follow run.log, or the journal on a systemd host
[group('host')]
logs target: build-desktop
    build/flocc-setup host {{target}} 'if [ -d /run/systemd/system ]; then journalctl --user -u flocc -f; else tail -f {{runlog}}; fi'

# Attach read-only to a tmux session on the host
[group('host')]
attach target session: build-desktop
    build/flocc-setup host {{target}} tmux -L flocc attach -r -t {{session}}

# Run one tick by hand on the host
[group('host')]
tick target: build-desktop
    build/flocc-setup host {{target}} flocc tick

# Release a blocked or timed-out issue's claim on the host
[group('host')]
[positional-arguments]
release target key *flags: build-desktop
    t=$1; shift; build/flocc-setup host "$t" flocc release "$@"

# Create a Daytona sandbox that never auto-stops
[group('daytona')]
daytona-up name:
    daytona create --snapshot daytona-medium --name {{name}} --auto-stop 0

# Stop a Daytona sandbox
[group('daytona')]
daytona-stop name:
    daytona stop {{name}}

# Start a Daytona sandbox, then ship to it
[group('daytona')]
daytona-start name:
    daytona start {{name}}
    just ship daytona:{{name}}

# List Daytona sandboxes
[group('daytona')]
daytona-list:
    daytona list
