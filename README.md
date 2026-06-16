# clipboard-sync

基于 Go + NATS + JSON + YAML 的跨平台剪切板同步系统，用于在 Linux 和 Windows 之间同步文本与文件剪切板。

文件同步遵循以下原则：

- 复制文件时只同步元数据，不传文件内容
- 粘贴时才按需传输文件内容
- 所有控制消息和文件分块都只通过 NATS 传输
- Linux 使用 `FUSE` 虚拟文件目录承接文件管理器原生粘贴
- Windows 使用 Explorer 原生虚拟文件剪贴板承接资源管理器原生粘贴

## 功能概览

- 文本剪切板实时同步
- 文件复制时仅同步元数据
- 文件粘贴时按需拉取内容
- 支持大文件流式传输，不一次性加载到内存
- 分块消息使用 `file.chunk` / `file.complete`
- token 自动过期，默认 TTL 为 60 秒
- 使用 `device_id` 防止循环广播
- 并发安全，传输过程支持重复块忽略与缺块检测

## 目录结构

```text
clipboard-sync/
  cmd/agent/
  internal/
  clipboard/
  mq/
  protocol/
  transfer/
  chunk/
  cache/
  device/
  configs/
```

## 工作原理

### 文本同步

1. 本地设备监听到文本剪切板变化
2. 发布 `clipboard.update`
3. 远端设备收到消息后直接写入本地系统剪切板

### 文件同步

1. 本地复制文件
2. agent 计算文件名、大小、SHA256，并生成 token
3. 发布 `clipboard.update`，只包含元数据和 token
4. 远端设备收到元数据后：
   Linux 把文件映射到本地 FUSE 虚拟目录，并把这些虚拟文件路径写入系统剪切板
   Windows 把文件注册成 Explorer 可读取的虚拟文件剪贴板对象
5. 用户在文件管理器或资源管理器中直接粘贴
6. 文件管理器开始读取文件时，agent 才发布 `clipboard.request`
7. 源端收到请求后，通过 NATS 按块发送文件数据
8. 目标端边接收边写 spool 文件，读侧阻塞等待所需字节到达
9. 传输完成后校验大小与 SHA256

## 消息主题

- `clipboard.update`
- `clipboard.request`
- `file.chunk`
- `file.complete`

## 环境要求

### 通用要求

- Go 版本：`1.22` 或更高
- NATS Server：建议 `2.9+`
- 两台设备网络互通，且都能访问同一个 NATS 服务地址
- 两端系统时间尽量同步，避免定位 token 过期问题时产生歧义

### Linux 要求

- `xclip` 或 `wl-clipboard`
- `fuse3`
- 文件管理器需支持标准文件路径粘贴

常见桌面环境：

- X11：使用 `xclip`
- Wayland：使用 `wl-copy` / `wl-paste`

### Windows 要求

- Windows 10 / Windows 11
- Explorer 作为文件粘贴目标
- PowerShell 或 CMD 可执行 agent

## 环境配置

### 1. 安装 Go

确认 Go 可用：

```bash
go version
```

输出应不低于 `go1.22`。

### 2. 安装 NATS Server

如果本机已经安装：

```bash
nats-server -v
```

如果未安装，请先安装 NATS Server，并确保 `nats-server` 已加入 `PATH`。

### 3. Linux 依赖安装

Debian / Ubuntu：

X11 环境：

```bash
sudo apt-get update
sudo apt-get install -y xclip fuse3
```

Wayland 环境：

```bash
sudo apt-get update
sudo apt-get install -y wl-clipboard fuse3
```

检查命令可用性：

```bash
which xclip
which wl-copy
which wl-paste
```

至少需要一组可用：

- `xclip`
- 或 `wl-copy` + `wl-paste`

检查 FUSE：

```bash
which fusermount3
```

### 4. Windows 环境准备

PowerShell 中确认 Go：

```powershell
go version
```

确认可以连接 NATS 所在地址，例如：

```powershell
Test-NetConnection -ComputerName 127.0.0.1 -Port 4222
```

如果 NATS 在远程机器，把 `127.0.0.1` 改成对应 IP 或主机名。

## 配置文件

示例配置见 `configs/config.yaml:1`。

推荐为每台设备准备独立配置，例如：

- `configs/linux.yaml`
- `configs/windows.yaml`

### 配置示例

```yaml
device_id: "device-A"
nats_url: "nats://localhost:4222"
chunk_size: 1048576
token_ttl: 60
poll_interval_ms: 500
cache_dir: ""
download_dir: ""
mount_dir: ""
log_level: "info"
```

### 配置项说明

- `device_id`
  每台设备唯一标识，必须不同

- `nats_url`
  NATS 服务地址，例如 `nats://192.168.1.10:4222`

- `chunk_size`
  单个文件分块大小，单位字节
  建议默认 `1048576` 即 `1MB`

- `token_ttl`
  文件 token 生存时间，单位秒
  默认 `60`

- `poll_interval_ms`
  剪切板轮询间隔，单位毫秒
  默认 `500`

- `cache_dir`
  状态缓存目录
  留空时自动使用系统临时目录

- `download_dir`
  远程文件 spool 和落地文件目录
  留空时自动使用系统临时目录

- `mount_dir`
  Linux FUSE 挂载目录
  留空时自动使用系统临时目录

- `log_level`
  日志级别，可选 `debug`、`info`、`warn`、`error`

### 推荐配置示例

Linux 设备：

```yaml
device_id: "linux-dev"
nats_url: "nats://192.168.1.100:4222"
chunk_size: 1048576
token_ttl: 60
poll_interval_ms: 500
cache_dir: "/tmp/clipboard-sync/cache"
download_dir: "/tmp/clipboard-sync/downloads"
mount_dir: "/tmp/clipboard-sync/mount"
log_level: "info"
```

Windows 设备：

```yaml
device_id: "windows-dev"
nats_url: "nats://192.168.1.100:4222"
chunk_size: 1048576
token_ttl: 60
poll_interval_ms: 500
cache_dir: "C:\\Temp\\clipboard-sync\\cache"
download_dir: "C:\\Temp\\clipboard-sync\\downloads"
mount_dir: ""
log_level: "info"
```

## 编译

Linux 本机构建：

```bash
go build ./cmd/agent
```

Windows 交叉编译：

```bash
GOOS=windows GOARCH=amd64 go build ./cmd/agent
```

## 启动 NATS

本机启动：

```bash
nats-server -p 4222
```

如果要让局域网其他设备访问，确认：

- 防火墙已放通 `4222`
- `nats_url` 指向实际可访问地址

验证 NATS 端口监听：

Linux：

```bash
ss -lntp | grep 4222
```

Windows：

```powershell
netstat -ano | findstr 4222
```

## 运行方式

### Linux 运行

启动 agent：

```bash
go run ./cmd/agent run -config configs/config.yaml
```

或使用构建后的二进制：

```bash
./agent run -config configs/config.yaml
```

Linux 启动后行为：

- 自动检测 `wl-clipboard` 或 `xclip`
- 自动挂载 FUSE 虚拟目录
- 监听本地剪切板变化
- 订阅 NATS 消息

### Windows 运行

PowerShell 启动 agent：

```powershell
go run .\cmd\agent run -config configs\config.yaml
```

或运行构建后的可执行文件：

```powershell
.\agent.exe run -config configs\config.yaml
```

Windows 启动后行为：

- 监听本地剪切板变化
- 订阅 NATS 消息
- 收到远程文件元数据时，将其注册为 Explorer 虚拟文件剪贴板对象

## 使用方法

### 文本同步

1. 在设备 A 复制一段文本
2. 设备 A 发布 `clipboard.update`
3. 设备 B 自动收到文本并写入本地剪切板
4. 在设备 B 直接粘贴即可

### 文件同步：Linux -> Windows

1. 在 Linux 设备上复制一个或多个文件
2. Linux agent 计算元数据并发布 `clipboard.update`
3. Windows agent 收到消息后，把这些文件注册为 Explorer 虚拟文件
4. 在 Windows 资源管理器中打开目标目录，直接按 `Ctrl+V`
5. 资源管理器会显示原生复制进度框
6. 真正读取文件内容时，Windows agent 才发起 `clipboard.request`
7. Linux 端开始通过 NATS 分块发送文件内容

### 文件同步：Windows -> Linux

1. 在 Windows 设备上复制一个或多个文件
2. Windows agent 计算元数据并发布 `clipboard.update`
3. Linux agent 收到消息后，把这些文件映射到 FUSE 虚拟目录，并把这些路径写入系统剪切板
4. 在 Linux 文件管理器中打开目标目录，直接按 `Ctrl+V`
5. 文件管理器会显示原生复制进度
6. 真正读取文件内容时，Linux agent 才发起 `clipboard.request`
7. Windows 端开始通过 NATS 分块发送文件内容

### 文件同步：Linux -> Linux / Windows -> Windows

只要两端都运行本项目并连接同一个 NATS，也按同样方式工作。

## 联调步骤

建议按下面顺序验证：

### 1. 单机验证 NATS

启动 NATS：

```bash
nats-server -p 4222
```

启动 agent，观察日志中是否报连接错误。

### 2. 文本同步验证

1. 两台机器都启动 agent
2. 在设备 A 复制文本
3. 到设备 B 粘贴
4. 确认文本内容一致

### 3. 小文件验证

1. 在设备 A 准备一个小文件，例如 `hello.txt`
2. 复制该文件
3. 在设备 B 的文件管理器里粘贴
4. 确认出现原生复制进度
5. 确认落地文件大小和内容正确

### 4. 大文件验证

1. 准备一个大于 `1GB` 的文件
2. 复制并跨设备粘贴
3. 观察内存占用是否稳定
4. 观察复制过程是否持续输出进度

## 日志说明

默认日志级别为 `info`。

常见日志含义：

- `agent started`
  agent 启动成功

- `clipboard text update published`
  本地文本变化已广播

- `clipboard file metadata published`
  本地文件复制元数据已广播

- `remote transfer requested`
  目标端在真正读取文件时发起了内容请求

- `file sent`
  源端发送完某个文件

- `file transfer completed`
  目标端完成某个文件的接收与校验

建议联调时把 `log_level` 设为 `debug`。

## 常见问题

### 1. Linux 启动时报找不到剪切板命令

表现：

- 提示未找到 `xclip`
- 或未找到 `wl-copy` / `wl-paste`

处理：

- X11 安装 `xclip`
- Wayland 安装 `wl-clipboard`

### 2. Linux 无法挂载 FUSE

处理：

- 安装 `fuse3`
- 确认系统允许当前用户使用 FUSE
- 检查 `mount_dir` 所在目录是否可写

### 3. 两台设备都启动了，但没有同步

检查：

- `device_id` 是否重复
- `nats_url` 是否一致且可访问
- NATS 端口是否被防火墙拦截
- 两端日志是否有连接错误

### 4. 文件元数据同步了，但粘贴时没反应

Linux 检查：

- 是否是在文件管理器中粘贴，而不是纯文本输入框
- FUSE 挂载是否成功
- 剪切板里是否已变成虚拟文件路径

Windows 检查：

- 是否在资源管理器中粘贴
- 资源管理器是否为当前前台上下文
- agent 是否正在运行且未退出

### 5. 复制大文件时速度慢

可以尝试：

- 增大 `chunk_size`，例如改为 `4194304`
- 保证 NATS 部署在低延迟网络中
- 避免源文件位于高延迟网络盘

### 6. token 过期

如果复制后长时间未粘贴，源端 token 可能过期。

处理：

- 重新复制文件
- 或增大 `token_ttl`

## 生产使用建议

- 为每台设备分配稳定且唯一的 `device_id`
- 把 `nats_url` 指向固定的内网 NATS 服务
- 把 `cache_dir`、`download_dir`、`mount_dir` 指向稳定目录
- 大文件场景下建议监控磁盘空间，因为 spool 文件会先写本地
- 建议用 `systemd` 或 Windows 计划任务把 agent 做成开机自启

## 开机自启建议

### Linux systemd

可创建一个 systemd service，执行：

```bash
/path/to/agent run -config /path/to/config.yaml
```

### Windows 计划任务

可将下面命令配置为登录后自动启动：

```powershell
C:\path\to\agent.exe run -config C:\path\to\config.yaml
```

## 开发与验证命令

格式化：

```bash
gofmt -w cache chunk clipboard cmd device internal mq protocol transfer
```

整理依赖：

```bash
go mod tidy
```

Linux 编译：

```bash
go build ./cmd/agent
```

Windows 交叉编译：

```bash
GOOS=windows GOARCH=amd64 go build ./cmd/agent
```
