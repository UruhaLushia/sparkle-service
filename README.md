# sparkle-service

sparkle-service 是 [Sparkle](https://github.com/xishang0128/sparkle) 的后台系统服务组件，使用 Go 编写。它以系统服务的形式运行，通过 Unix Socket（Linux/macOS）或命名管道（Windows）对外提供 HTTP API，负责管理代理核心进程的生命周期、系统代理设置和 DNS 配置等。

## 功能特性

- **核心进程管理**：启动、停止、重启并监控代理核心进程（如 mihomo），支持崩溃自动恢复
- **系统代理设置**：通过命令行或 HTTP API 设置 / 清除系统代理（支持普通代理和 PAC）
- **DNS 配置**：通过 API 为指定网络设备设置 DNS 服务器
- **系统服务管理**：将自身注册为系统服务（支持 Windows、Linux、macOS），并提供安装、卸载、启停控制
- **身份验证**：基于 Ed25519 公钥签名的请求认证，支持授权主体绑定，保障 API 安全
- **事件推送**：通过 SSE（Server-Sent Events）实时推送核心进程状态变更和系统代理状态变更事件

## 平台支持

| 平台    | 传输方式   | 默认监听地址                        |
| ------- | ---------- | ----------------------------------- |
| Windows | 命名管道   | `\\.\pipe\sparkle\service`          |
| Linux   | Unix Socket | `/tmp/sparkle-service.sock`        |
| macOS   | Unix Socket | `/tmp/sparkle-service.sock`        |

## 快速开始

### 构建

```bash
go build -o sparkle-service .
```

### 作为系统服务运行（推荐）

**安装并启动服务：**

```bash
# Windows（需管理员权限）
sparkle-service.exe service install

# Linux / macOS（需 root 权限）
sudo ./sparkle-service service install
```

**卸载服务：**

```bash
sparkle-service service uninstall
```

**查看服务状态：**

```bash
sparkle-service service status
```

### 测试模式运行

以前台模式在 `127.0.0.1:10002` 启动 HTTP 服务（仅用于开发调试）：

```bash
sparkle-service server
```

## 命令行参考

```
sparkle-service [全局选项] <命令> [命令选项]
```

### 全局选项

| 选项                    | 简写 | 默认值                       | 说明                   |
| ----------------------- | ---- | ---------------------------- | ---------------------- |
| `--listen`              | `-l` | 平台默认套接字/管道地址      | 指定监听地址           |
| `--device`              | `-d` | （空，使用系统默认）         | 指定网络设备名称       |
| `--only-active-device`  | `-a` | `false`                      | 仅对活跃网络设备生效   |
| `--use-registry`        | `-r` | `false`                      | 使用注册表设置（Windows）|

### 子命令

| 命令                      | 说明                               |
| ------------------------- | ---------------------------------- |
| `sysproxy proxy -s <地址> [-b <绕过>]` | 设置系统代理              |
| `sysproxy pac -u <PAC地址>` | 设置 PAC 代理                    |
| `sysproxy disable`        | 取消系统代理设置                   |
| `sysproxy status`         | 查看当前代理状态                   |
| `server`                  | 前台启动 HTTP 服务（测试用）       |
| `service install`         | 安装并启动系统服务                 |
| `service uninstall`       | 停止并卸载系统服务                 |
| `service start`           | 启动已安装的服务                   |
| `service stop`            | 停止运行中的服务                   |
| `service restart`         | 重启服务                           |
| `service status`          | 查看服务当前状态                   |

**示例：**

```bash
# 设置系统代理
sparkle-service sysproxy proxy -s 127.0.0.1:7890 -b "localhost;127.*;10.*;192.168.*"

# 设置 PAC 代理
sparkle-service sysproxy pac -u http://127.0.0.1:7890/pac

# 取消代理
sparkle-service sysproxy disable

# 查看代理状态
sparkle-service sysproxy status
```

## HTTP API

服务启动后监听本地套接字/管道，所有 API 均为 JSON 格式。

> **注意**：除 `/ping` 外，所有接口均需要 Ed25519 签名认证。

### 健康检查

```
GET /ping
```

### 核心进程 `/core`

| 方法   | 路径               | 说明                         |
| ------ | ------------------ | ---------------------------- |
| GET    | `/core/`           | 获取核心进程状态             |
| GET    | `/core/events`     | SSE 订阅核心状态变更事件     |
| GET    | `/core/profile`    | 获取启动配置（Profile）      |
| POST   | `/core/profile`    | 保存启动配置                 |
| PATCH  | `/core/profile`    | 部分更新启动配置             |
| POST   | `/core/start`      | 启动核心进程                 |
| POST   | `/core/stop`       | 停止核心进程                 |
| POST   | `/core/restart`    | 重启核心进程                 |
| ANY    | `/core/controller` | 透传至核心控制器接口         |

`GET /core/` 返回运行中的核心 PID、内存、CPU 使用率和实际 CPU 绑定范围；`GET /core/profile` 返回配置中的 `cpu_affinity`。

运行中的核心通过 `PATCH /core/profile` 更新 `cpu_affinity` 会立即生效；传入空数组会恢复 service 当前允许的 CPU 集合。

**启动配置（LaunchProfile）字段：**

```json
{
  "core_path": "/path/to/mihomo",
  "args": ["--config", "/etc/mihomo/config.yaml"],
  "safe_paths": ["/etc/mihomo"],
  "env": { "KEY": "value" },
  "cpu_affinity": [2, 3],
  "cpu_priority": "normal",
  "log_path": "/var/log/sparkle/core.log",
  "save_logs": true,
  "max_log_file_size_mb": 10
}
```

`cpu_affinity` 使用从 0 开始的逻辑 CPU 编号。Windows 和 Linux 核心进程都会应用该绑定；Windows 受系统处理器组和进程亲和性掩码限制，当前支持单组内的 CPU 编号。

### 系统代理 `/sysproxy`

| 方法   | 路径                  | 说明                     |
| ------ | --------------------- | ------------------------ |
| GET    | `/sysproxy/status`    | 查询当前代理设置         |
| GET    | `/sysproxy/events`    | SSE 订阅代理状态变更事件 |
| POST   | `/sysproxy/proxy`     | 设置系统代理             |
| POST   | `/sysproxy/pac`       | 设置 PAC 代理            |
| POST   | `/sysproxy/disable`   | 取消代理设置             |

**请求体示例（设置代理）：**

```json
{
  "server": "127.0.0.1:7890",
  "bypass": "localhost;127.*;10.*;192.168.*",
  "device": "",
  "only_active_device": false,
  "use_registry": false,
  "guard": true
}
```

### 系统 `/sys`

| 方法   | 路径           | 说明         |
| ------ | -------------- | ------------ |
| GET    | `/sys/cpu`     | 获取可用于核心绑定的 CPU 信息 |
| POST   | `/sys/dns/set` | 设置 DNS     |

`GET /sys/cpu` 返回逻辑 CPU 编号、物理核心信息、型号、可用状态和整数 `core_class`。Windows 返回原始 `EfficiencyClass`；Linux 优先返回非零 `topology/core_type`，否则读取 `cpu_capacity`。名称和分组由前端判断，数值不可跨平台比较；无法读取时返回 0（Windows 的有效等级也可能为 0）。`available: true` 表示 service 当前允许使用该 CPU，可直接用于 `cpu_affinity`；`affinity_supported: false` 表示当前平台只能提供 CPU 列表，无法确认 service 的实际亲和性限制。

**请求体示例：**

```json
{
  "device": "eth0",
  "servers": ["8.8.8.8", "8.8.4.4"]
}
```

### 服务控制 `/service`

| 方法   | 路径               | 说明           |
| ------ | ------------------ | -------------- |
| POST   | `/service/stop`    | 停止服务       |
| POST   | `/service/restart` | 重启服务       |

## 身份验证

每个受保护的请求须通过**两层校验**才能被接受：

### 第一层：请求方身份（Principal）校验

服务通过操作系统提供的传输层身份识别调用方进程，并与预先绑定的授权主体进行比对：

| 平台    | 识别方式               | 授权主体类型 |
| ------- | ---------------------- | ------------ |
| Windows | 命名管道客户端 SID     | `sid`        |
| Linux   | Unix Socket 对端 UID   | `uid`        |
| macOS   | Unix Socket 对端 UID   | `uid`        |

授权主体绑定文件位于 `<配置目录>/sparkle/keys/authorized_principal.json`，格式如下：

```json
{ "type": "sid", "value": "S-1-5-21-..." }
```

若该文件不存在或未配置，所有受保护接口将返回 `503 Service Unavailable`；若调用方身份与绑定值不符，则返回 `403 Forbidden`。

### 第二层：Ed25519 签名（Auth V2）

通过身份校验后，服务还会验证请求头中的 Ed25519 签名，以确认请求未被篡改且不是重放攻击。

**必须携带的请求头：**

| 请求头              | 说明                                         |
| ------------------- | -------------------------------------------- |
| `X-Auth-Version`    | 固定为 `2`                                   |
| `X-Timestamp`       | 请求时间（毫秒级 Unix 时间戳）               |
| `X-Nonce`           | 随机字符串，防止重放                         |
| `X-Content-SHA256`  | 请求体的 SHA-256 十六进制摘要（小写）        |
| `X-Key-Id`          | 公钥 ID（公钥 DER 字节的 SHA-256 十六进制值）|
| `X-Signature`       | 对规范化请求字符串的 Ed25519 签名（Base64）  |

**校验规则：**

- 时间戳与服务器时间偏差不得超过 **±30 秒**
- Nonce 在时间窗口内不可重复使用（防重放）
- 请求体实际 SHA-256 摘要须与 `X-Content-SHA256` 一致
- 签名须能被已注册的公钥（`current` 或 `previous`）验证通过

**公钥存储文件** `<配置目录>/sparkle/keys/public_keys.json` 格式：

```json
{
  "current":  { "key_id": "<sha256-hex>", "public_key": "<base64-DER>" },
  "previous": { "key_id": "<sha256-hex>", "public_key": "<base64-DER>" }
}
```

支持 `current` 和 `previous` 两个公钥，便于客户端无缝轮换密钥。

配置目录位置：

| 平台    | 路径                                           |
| ------- | ---------------------------------------------- |
| Windows | `C:\ProgramData`                               |
| macOS   | `/var/root/Library/Application Support`        |
| Linux   | `/root/.config`                                |

也可通过环境变量 `SPARKLE_CONFIG_DIR` 自定义配置目录。

## 依赖

| 依赖                       | 用途             |
| -------------------------- | ---------------- |
| go-chi/chi                 | HTTP 路由        |
| kardianos/service          | 系统服务管理     |
| shirou/gopsutil            | 进程信息获取     |
| spf13/cobra                | CLI 框架         |
| UruhaLushia/sysproxy-go    | 系统代理设置     |
| go.uber.org/zap            | 结构化日志       |

## 许可证

本项目以 [LICENSE](LICENSE) 文件中指定的许可证开源。
