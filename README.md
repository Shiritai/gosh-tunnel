# Gosh-Tunnel 🚀

Gosh-Tunnel is a high-performance, highly available SSH Tunnel (Port Forwarding) manager written in Go. It adopts a modern **Daemon + CLI** architecture designed specifically to solve the pain points of managing "massive ports", "automatic reconnections", and "zero-downtime hot reloading" that traditional `ssh -N -f -L` commands and scripts suffer from.

## 🌟 Core Features

1. **Native `~/.ssh/config` Support**: No need to redefine keys, users, or jump hosts. It seamlessly inherits your existing system SSH configurations.
2. **Multi-Port and Range Mapping**: Supports mapping single ports (`8080:80`) or entire bulk ranges (`8080-8084:8080-8084`) simultaneously.
3. **Bulletproof Auto-Reconnect**: Built on Go's concurrent engine with KeepAlive mechanisms. When your laptop wakes from sleep or switches Wi-Fi networks, it instantly and automatically reconnects all tunnels in the background.
4. **Unix Socket IPC Hot-Plugging**: No service restarts, no file polling. Instantly `add` or `rm` port mappings via quick CLI commands. Adding a new port **does not interrupt** traffic on existing active tunnels!

## 🛠️ Installation & Build

### Build Steps
If you have the source code locally, simply run:

```bash
# 1. Initialize and download dependencies
go mod download
go mod tidy

# 2. Build the 'gosh' CLI binary
go build -o gosh .
```

## 📖 Configuration

Gosh uses a simple YAML file to define the initial state. Create a `config.yaml`:

```yaml
# Defaults to parsing your system config. You can specify a custom path.
ssh_config: "~/.ssh/config" 

tunnels:
  - server: "gpu-server"        # Must match a Host alias in ~/.ssh/config
    ports: 
      - "8080-8084:8080-8084"  # Range Mapping (Local : Remote)
      - "8080:80"                  # Single Mapping
      
  - server: "workstation"
    ports:
      - "9090-9092:9090-9092"
```

## 🚀 CLI Usage

Gosh provides an elegant command-line interface powered by Cobra:

### 1. Start the Daemon
This command launches Gosh in the background, establishing the initial connections defined in your `config.yaml`.
```bash
./gosh start -c config.yaml
```

### 2. Query Tunnel Status
```bash
./gosh status
```

### 3. Hot-Add a New Port
```bash
./gosh add 1234 gpu-server:80
```

### 4. Hot-Remove a Port
```bash
./gosh rm gpu-server-1234:80
```

---

## 👑 The Ultimate "Zero-Touch" Setup

See the [macOS Section](#-macos-launchd-natively) or [Linux Section](#-linux-systemd-user-service) below to register Gosh-Tunnel as a background service.

### 🍎 macOS (launchd) natively
1. Create a `plist` file at `~/Library/LaunchAgents/com.user.goshtunnel.plist`:
*(Make sure to use absolute paths for the binary and config)*
```xml
...
```

### 🐧 Linux (Systemd User Service)
1. Create `~/.config/systemd/user/gosh-tunnel.service`:
```ini
...
```
