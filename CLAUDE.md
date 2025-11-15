# CLAUDE.md

This file provides guidance to AI assistants when working with code in this repository.

## Project Overview

`ssm-ssh-connect` is a Go-based SSH proxy command that enables keyless SSH access to EC2 instances through AWS Session Manager. It bridges the gap between AWS Session Manager's WebSocket connections and traditional SSH tooling by acting as a ProxyCommand in SSH configurations.

The tool automatically:
- Resolves EC2 instance IDs from instance names (with 24h caching)
- Pushes SSH public keys to instances via EC2 Instance Connect (60s TTL)
- Establishes SSM sessions through the `session-manager-plugin`

## Architecture

### Single-File Design
All code lives in `main.go` (370 lines). The application is intentionally monolithic to simplify distribution as a standalone binary.

### Key Components

1. **Caching System** (lines 150-203)
   - Instance metadata cached in `~/.ssm-ssh-connect/<profile>-<instance>-<user>.json`
   - 24-hour TTL for cache entries
   - Reduces AWS API calls for repeated connections

2. **Lock File Mechanism** (lines 103-139)
   - Prevents duplicate SSH key pushes during concurrent connections
   - Lock files: `~/.ssm-ssh-connect/<profile>-<instance>-<user>.lock`
   - Auto-cleanup after 50 seconds (SSH keys valid for 60s)
   - Critical for handling multiple SSH sessions to same instance

3. **AWS Integration Flow**
   - EC2: Queries instances by Name tag (main:221-248)
   - EC2 Instance Connect: Sends SSH public keys (main:250-271)
   - SSM: Starts SSH session via `AWS-StartSSHSession` document (main:285-369)

4. **Session Manager Plugin Invocation** (main:328-356)
   - Locates `session-manager-plugin` binary in common paths
   - Argument order is critical (matches AWS SDK expectations)
   - Passes session metadata as JSON structures

5. **Parent Process Monitoring** (main:365-406, added in v0.2.0)
   - Detects orphaned sessions when SSH client dies unexpectedly
   - Checks every 1 hour if parent process (SSH) is still alive
   - Kills `session-manager-plugin` if parent PID changes or becomes 1
   - Prevents accumulation of zombie processes

### Critical Constraint: Cannot Monitor stdin Directly

**DO NOT** attempt to detect SSH disconnect by reading from `os.Stdin`. This was attempted and breaks SSH sessions.

**Why it fails:**
```go
// BROKEN APPROACH - DO NOT USE
go func() {
    io.Copy(io.Discard, os.Stdin)  // Consumes stdin data!
    // Detect close...
}()
```

When `cmd.Stdin = os.Stdin` is set (line 360), the `session-manager-plugin` process expects to read SSH data from stdin. If another goroutine also reads from `os.Stdin`, both compete for the data stream, causing:
- SSH session hangs immediately after connection
- Commands typed by user are lost/discarded
- Session becomes unresponsive

**Why `cmd.Run()` must stay in a goroutine:**
- Original `cmd.Run()` blocks until plugin exits
- Running in goroutine allows concurrent parent process monitoring
- Select statement handles both normal exit and parent death
- Preserves stdin/stdout/stderr passthrough to plugin

## Commands

### Build
```bash
go build -o ssm-ssh-connect
```

Cross-compile for macOS (Intel):
```bash
GOOS=darwin GOARCH=amd64 go build -o ssm-ssh-connect
```

Cross-compile for macOS (ARM):
```bash
GOOS=darwin GOARCH=arm64 go build -o ssm-ssh-connect
```

### Testing
Manual testing requires:
```bash
# Set debug logging
export SSM_SSH_CONNECT_DEBUG=1

# Test connection (invoked via SSH)
ssh -v <instance-name>

# View logs
tail -f ~/.ssm-ssh-connect/ssm-ssh-connect.log
```

No unit tests exist. Testing is done through SSH connections.

Integration tests:
```bash
# Test 1: Successful command execution
ssh -v ssm-test true && echo "ok"

# Test 2: Failed command execution (expect non-zero exit)
ssh -v ssm-test false || echo "ok"

# Test 3: Long-running command
ssh -v ssm-test "sleep 5" && echo "ok"
```

### Release
Releases are automated via GitHub Actions (`.github/workflows/release.yml`):
```bash
git tag v1.x.x
git push origin v1.x.x
```

## Important Implementation Details

### Logging Configuration (main:65-73)
- Default log level: `ERROR` (to minimize noise in SSH sessions)
- Enable debug: `export SSM_SSH_CONNECT_DEBUG=1`
- Log rotation: Files >1MB are auto-deleted on next run
- All logs in `~/.ssm-ssh-connect/ssm-ssh-connect.log`

### Signal Handling (main:205-219)
- `SIGHUP` is ignored (SSH may send during connection setup)
- `SIGINT`/`SIGTERM` trigger graceful shutdown
- Important for proper cleanup of sessions

### SSH Public Key Handling
- Reads from `~/.ssh/id_rsa.pub` only (hardcoded)
- Key pushed via EC2 Instance Connect API
- Valid for 60 seconds on target instance
- Lock file prevents race conditions

### Error Handling Philosophy
- AWS SDK errors logged via slog but don't always exit
- Critical errors (instance not found, plugin missing) cause exit(1)
- Designed to fail gracefully for SSH's benefit

## Dependencies
- AWS SDK v2 for Go (EC2, SSM, EC2 Instance Connect)
- Requires `session-manager-plugin` binary installed separately
- Reads AWS profiles from standard AWS config files

## Usage Context
Expected to be invoked via SSH ProxyCommand:
```
Host prd-* qa-* dev-*
User ubuntu
ProxyCommand /path/to/ssm-ssh-connect <aws-profile> %h %r
```

Where `%h` = hostname (instance name), `%r` = remote user
