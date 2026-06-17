# clipboard-sync

基于 Go + NATS 的跨平台剪贴板同步工具，用于在 Linux 和 Windows 之间同步文本、图片与文件剪贴板。

文件同步遵循按需传输原则：复制文件时只同步元数据，真正粘贴并读取文件时才通过 NATS 分块传输文件内容。

## 功能概览

- 文本剪贴板同步
- 图片剪贴板同步
- 文件复制时只同步元数据
- 文件粘贴时按需拉取内容
- Linux 通过 FUSE 虚拟目录承接文件管理器原生粘贴
- Windows 通过 Explorer 虚拟文件剪贴板承接资源管理器原生粘贴
- 多设备通过 `group_id` 分组
- 使用 `device_id` 防止循环同步

## 工作原理

### 文本同步

1. 本地 agent 监听到文本剪贴板变化
2. 发布 `clipboard.update`
3. 远端 agent 收到消息后写入本地系统剪贴板
4. 用户直接粘贴

### 文件同步

1. 设备 A 复制文件
2. 设备 A 的 agent 生成文件元数据和 token
3. agent 通过 NATS 发布 `clipboard.update`
4. 设备 B 的 agent 收到元数据
5. Linux 端把远程文件映射到 FUSE 虚拟目录，并把虚拟路径写入文件剪贴板
6. Windows 端把远程文件注册为 Explorer 可读取的虚拟文件对象
7. 用户在文件管理器或资源管理器中粘贴
8. 文件管理器真正读取内容时，目标端发布 `clipboard.request`
9. 源端通过 NATS 发送文件分块
10. 目标端边接收边写 spool 文件，并完成大小与 SHA256 校验

## 环境要求

### 通用要求

- Go `1.22` 或更高，仅开发/构建时需要
- NATS Server，建议 `2.9+`
- 两端设备网络互通，并能访问同一个 NATS 服务地址
- 两端 `group_id` 一致
- 每台设备的 `device_id` 必须不同

### Linux 要求

所有 Linux 桌面都需要：

- `fuse3`
- X11 环境需要 `xclip`
- Wayland 环境需要 `wl-clipboard`，即 `wl-copy` / `wl-paste`
- 文件管理器需要支持标准文件路径粘贴

Debian / Ubuntu：

```bash
sudo apt-get update
sudo apt-get install -y fuse3 xclip
```

Wayland 环境改装：

```bash
sudo apt-get update
sudo apt-get install -y fuse3 wl-clipboard
```

Rocky / RHEL / CentOS：

```bash
sudo dnf install -y fuse3 xclip
```

如果使用 GTK 文件剪贴板后端，还需要安装 GTK Python 绑定。

Debian / Ubuntu：

```bash
sudo apt-get install -y python3-gi gir1.2-gtk-3.0
```

Rocky / RHEL / CentOS：

```bash
sudo dnf install -y python3-gobject gtk3
```

检查依赖：

```bash
which xclip
which wl-copy
which wl-paste
which fusermount3
```

X11 至少需要 `xclip` 可用；Wayland 至少需要 `wl-copy` 和 `wl-paste` 可用。

### Windows 要求

- Windows 10 / Windows 11
- Explorer 作为文件粘贴目标
- PowerShell 或 CMD 可执行 agent

## Linux 文件剪贴板后端

Linux 端收到远程文件元数据后，需要把 FUSE 虚拟文件路径写入系统文件剪贴板。不同 Linux 桌面环境对文件剪贴板的接受方式不同，因此提供 `clipboard_file_writer` 配置项。

```yaml
clipboard_file_writer: "native"
```

可选值：

| 值 | 行为 | 适用场景 |
|---|---|---|
| `native` 或留空 | 使用原生命令写文件剪贴板。X11 使用 `xclip`，Wayland 使用 `wl-copy` / `wl-paste` | 大多数现代 Linux 桌面环境 |
| `gtk` | 使用 GTK helper 进程持有剪贴板，并写入 Nautilus 兼容格式 | GNOME / Nautilus 3.x，例如 Rocky Linux 8、RHEL 8、CentOS 8 |
| `auto` | 优先尝试 GTK helper，可用则使用 GTK，否则回退 native | 不确定目标桌面环境时可用 |

说明：

- `clipboard_file_writer` 只影响 Linux 收到远程文件后如何写入文件剪贴板。
- 文本和图片同步仍走原有逻辑。
- GTK helper 已嵌入到 Go 二进制中，不需要单独分发 `gtk_helper.py`。
- 使用 `gtk` 时，运行机器必须安装 `python3-gobject` / GTK3。
- Rocky Linux 8 / Nautilus 3.28 建议使用 `clipboard_file_writer: "gtk"`，否则可能出现元数据已收到但 Nautilus 粘贴按钮不可用的问题。

## 构建说明

构建维度是 `操作系统 + CPU 架构`，不是 Linux 发行版。

也就是说，Rocky Linux、Ubuntu、Debian、Fedora 等 x86_64 Linux 通常可以共用同一个 `linux-amd64` 二进制。不同 Linux 平台的差异主要体现在运行时依赖和 `clipboard_file_writer` 配置上。

Linux x86_64 / amd64：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/agent-linux-amd64 ./cmd/agent
```

Linux arm64：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o dist/agent-linux-arm64 ./cmd/agent
```

Windows x86_64 / amd64：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o dist/agent-windows-amd64.exe ./cmd/agent
```

当前机器本机构建：

```bash
CGO_ENABLED=0 go build -o agent ./cmd/agent
```

说明：

- Linux 二进制使用 `CGO_ENABLED=0` 构建后，不依赖构建机 glibc 版本。
- 运行时仍需要安装对应剪贴板工具、FUSE，以及可选 GTK 依赖。
- 如果要发布 release，建议分别提供 `agent-linux-amd64`、`agent-linux-arm64`、`agent-windows-amd64.exe`。

## 配置文件

推荐为每台设备准备独立配置文件，例如：

- `configs/linux.yaml`
- `configs/windows.yaml`

### 通用配置示例

```yaml
device_id: "device-A"
group_id: "default"
nats_url: "nats://localhost:4222"
chunk_size: 8388608
token_ttl: 60
poll_interval_ms: 500
cache_dir: ""
download_dir: ""
mount_dir: ""
log_level: "error"
clipboard_file_writer: "native"
```

### 配置项说明

- `device_id`：每台设备唯一标识，必须不同。
- `group_id`：剪贴板同步分组，只有相同 `group_id` 的设备才会互相同步。
- `nats_url`：NATS 服务地址，例如 `nats://192.168.1.10:4222`。
- `chunk_size`：单个文件分块大小，单位字节，建议 `8388608` 即 `8MB`。
- `token_ttl`：文件 token 生存时间，单位秒，默认 `60`。
- `poll_interval_ms`：剪贴板轮询间隔，单位毫秒，默认 `500`。
- `cache_dir`：状态缓存目录，留空时自动使用系统临时目录。
- `download_dir`：远程文件 spool 和落地文件目录，留空时自动使用系统临时目录。
- `mount_dir`：Linux FUSE 挂载目录，留空时自动使用系统临时目录。
- `log_level`：日志级别，可选 `debug`、`info`、`warn`、`error`。
- `clipboard_file_writer`：Linux 文件剪贴板写入后端，可选 `native`、`gtk`、`auto`。

### Rocky Linux 8 / Nautilus 3.28 示例

```yaml
device_id: "linux-rocky8"
group_id: "default"
nats_url: "nats://192.168.1.100:4222"
chunk_size: 8388608
token_ttl: 60
poll_interval_ms: 500
cache_dir: ""
download_dir: ""
mount_dir: ""
log_level: "info"
clipboard_file_writer: "gtk"
```

Rocky Linux 8 / Nautilus 3.28 建议使用 `gtk` 后端。

### Windows 示例

```yaml
device_id: "windows-dev"
group_id: "default"
nats_url: "nats://192.168.1.100:4222"
chunk_size: 8388608
token_ttl: 60
poll_interval_ms: 500
cache_dir: "C:\\Temp\\clipboard-sync\\cache"
download_dir: "C:\\Temp\\clipboard-sync\\downloads"
mount_dir: ""
log_level: "error"
```

Windows 会忽略 `clipboard_file_writer`。

## 启动 NATS

本机启动：

```bash
nats-server -p 4222
```

如果局域网其他设备需要访问，确认：

- 防火墙已放通 `4222`
- `nats_url` 指向实际可访问地址

Linux 验证端口：

```bash
ss -lntp | grep 4222
```

Windows 验证端口：

```powershell
netstat -ano | findstr 4222
```

## 运行方式

### Linux 运行

Linux agent 必须以当前桌面用户身份运行，不能用 root 运行。否则可能无法访问用户的 X11/Wayland 剪贴板，也可能导致 FUSE 挂载权限不正确。

```bash
./agent run -config configs/linux.yaml
```

或开发模式：

```bash
go run ./cmd/agent run -config configs/linux.yaml
```

Linux 启动后行为：

- 自动检测 `wl-clipboard` 或 `xclip`
- 自动挂载 FUSE 虚拟目录
- 监听本地剪贴板变化
- 订阅 NATS 消息
- 收到远程文件元数据时，根据 `clipboard_file_writer` 写入文件剪贴板

### Windows 运行

```powershell
.\agent.exe run -config configs\windows.yaml
```

或开发模式：

```powershell
go run .\cmd\agent run -config configs\windows.yaml
```

Windows 启动后行为：

- 监听本地剪贴板变化
- 订阅 NATS 消息
- 收到远程文件元数据时，将其注册为 Explorer 虚拟文件剪贴板对象

## 使用方法

### 文本同步

1. 两端 agent 都启动并连接同一个 NATS。
2. 在设备 A 复制文本。
3. 到设备 B 粘贴。
4. 文本内容应保持一致。

### 文件同步：Linux 到 Windows

1. 在 Linux 文件管理器中复制一个或多个文件。
2. Linux agent 发布文件元数据。
3. Windows agent 收到元数据后注册 Explorer 虚拟文件对象。
4. 在 Windows 资源管理器目标目录按 `Ctrl+V`。
5. Explorer 读取文件内容时，Windows agent 向 Linux agent 请求实际内容。

### 文件同步：Windows 到 Linux

1. 在 Windows 资源管理器中复制一个或多个文件。
2. Windows agent 发布文件元数据。
3. Linux agent 收到元数据后映射到 FUSE 虚拟目录。
4. Linux agent 将虚拟路径写入系统文件剪贴板。
5. 在 Linux 文件管理器目标目录按 `Ctrl+V`。
6. 文件管理器读取虚拟文件时，Linux agent 向 Windows agent 请求实际内容。

## 联调步骤

建议按顺序验证：

1. 启动 NATS，并确认两端都能连接。
2. 两端启动 agent。
3. 先验证文本同步。
4. 再验证小文件同步。
5. 最后验证大文件同步。

小文件验证：

```text
设备 A 复制 hello.txt -> 设备 B 文件管理器 Ctrl+V -> 确认内容一致
```

大文件验证：

```text
准备大于 1GB 的文件 -> 跨设备复制粘贴 -> 观察内存和复制进度
```

## 常见问题

### Linux 启动时报找不到剪贴板命令

X11 安装 `xclip`。

Wayland 安装 `wl-clipboard`。

### Linux 无法挂载 FUSE

检查：

- 是否安装 `fuse3`
- 当前用户是否允许使用 FUSE
- `mount_dir` 所在目录是否可写
- agent 是否以桌面用户身份运行，而不是 root

### Windows 到 Linux 元数据已收到，但 Nautilus 不能粘贴

如果是 GNOME / Nautilus 3.x，尤其 Rocky Linux 8 / RHEL 8 / CentOS 8，建议启用 GTK 后端：

```yaml
clipboard_file_writer: "gtk"
```

并确认依赖已安装：

```bash
sudo dnf install -y python3-gobject gtk3
```

同时确认 agent 是在桌面用户会话中运行，不是 root。

### 两台设备都启动了，但没有同步

检查：

- `device_id` 是否重复
- `group_id` 是否一致
- `nats_url` 是否一致且可访问
- NATS 端口是否被防火墙拦截
- 两端日志是否有连接错误

### token 过期

如果复制后长时间未粘贴，源端 token 可能过期。

处理方式：

- 重新复制文件
- 或增大 `token_ttl`

## 开发与验证命令

格式化：

```bash
gofmt -w cache chunk clipboard cmd device internal mq protocol transfer
```

测试：

```bash
go test ./...
```

静态检查：

```bash
go vet ./...
```

检查补丁格式：

```bash
git diff --check
```

本机构建：

```bash
CGO_ENABLED=0 go build -o agent ./cmd/agent
```

Windows 交叉编译：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o agent.exe ./cmd/agent
```
