# QuickWork / Amazon Quick → OpenAI 兼容反代

本仓库基于对 `Amazon-Quick.exe` 解包后的 Electron 前端与 PyInstaller 后端的静态分析。

结论先说：

- **没有 email/password 登录，也没有上游注册 API。** 登录是浏览器 OAuth：consumer/social 走 AWS IAM Identity Center OIDC，企业走企业 OIDC discovery；注册只打开 `https://quick.aws.com/signup`。
- 聊天不是 HTTP REST，而是 WebSocket JSON frame；本仓库把它转换成 `POST /v1/chat/completions`（含 SSE）。
- `auth/quickauth.py` **原生实现** discovery + PKCE + token 交换，不启动 `quickwork-agent.exe`、不经过 outpost。登录得到的 token 存在 `data/upstream.json`，Go 反代可用的 session 写到 `data/session.json`。
- Go 反代支持用 `data/tokens.json` 做本地 API key 鉴权；文件不存在或为空时关闭鉴权。本工具不创建本地账号。

## 实现位置

| 内容 | 路径 |
|---|---|
| 原生 OAuth / session / refresh | `auth/quickauth.py` |
| OpenAI 兼容 HTTP / SSE | `internal/proxy/server.go`、`messages.go` |
| WebSocket 帧与 codec | `internal/proxy/upstream.go`、`wire.go` |
| session 与 outpost 兼容读取 | `internal/proxy/session.go` |
| 本地 API key 鉴权 | `internal/proxy/auth.go` |

关键事实：

- **原生登录（`auth/quickauth.py`）**：
  - social：浏览器打开 `https://quick.aws.com/sn?qwSurface=desktop:<nonce>:<port>:<appVersion>`；discovery 回调 `/callback` 使用 camelCase 参数（`issuerUrl`、`tenantUrl`、`idcInstanceId`、`idcRegion`）；随后 `POST https://oidc.<idcRegion>.amazonaws.com/client/register`，再 302 到 `/authorize`（PKCE S256），回调 `/` 收 code，`POST /token` 换 token。
  - enterprise：浏览器打开 `https://<region>.quicksight.aws.amazon.com/sn/start/app-integrations/quickwork-enterprise-login?qwSurface=...`；discovery 回调同样是 camelCase（`authEndpoint`、`tokenEndpoint`、`clientId`、`clientSecret`、`tenantUrl`）；再走企业 OIDC authorize + token endpoint。
  - token 用完后可 `python auth/quickauth.py refresh`，不需要重新打开浏览器。
- 原始 App 的 outpost 等价接口（仅作参考）：`POST {outpost}/auth/profile-login`，以及 `GET {outpost}/auth/sncs-session`（返回 `id_token`、`ws_host`、`ws_path`、`tenant_url`、`actor_arn`、`provider`、`surface`、`aws_account_id` 等）。原生工具直接产出同样的 session 字段。
- 聊天 URL：`wss://{ws_host}/streaming/quickwork-chat-bidirectional-stream`；先发 `session_connect`、`session_create`、`user_message`，收 `text_delta` / `message_complete` / `turn_completed`。
- AIDP（`enterprise`/`social`）帧是二进制 JSON 包裹 base64 内层 JSON，回包是 AWS event-stream；`gateway`/`social_gateway` 走 AWS event-stream + CBOR（codec 已做本地 round-trip 验证，但无真实凭证做端到端验证）。
- 没有 `/v1/models` 或模型列表 API；可用值为 agent mode：`smart`、`fast`、`balanced`（以及 `auto`）。

## 目录

```
quickwork-openai/
  cmd/quickproxy/          Go 反代入口
  internal/proxy/          session、WS codec、OpenAI handler
  auth/quickauth.py        Python 原生 OAuth 登录 + session/refresh
  data/                    运行期文件（已 gitignore）
  go.mod / go.sum
```

## 准备

```bash
cd quickwork-openai
go build -o bin/quickproxy.exe ./cmd/quickproxy
```

Go 反代在 `data/tokens.json` 不存在或为空时关闭鉴权。需要 API key 时，把 token 写进该文件（格式见 `internal/proxy/auth.go`）；不要提交 `data/`。

## 上游登录（原生 Python，无 outpost）

```bash
python auth/quickauth.py upstream-login --provider social --build-stage prod
```

- `--provider social`：consumer/social（AWS IAM Identity Center OIDC），默认。
- `--provider enterprise`：企业 SSO；可加 `--region us-east-1` 指定 discovery 前缀。
- `--build-stage prod|beta|integ|dev`：默认 `prod`。
- `--port`：默认 `18080`；浏览器回调必须能访问这个本机端口。
- `--open`：可选，自动打开默认浏览器；默认只打印 URL，由用户自己打开。

流程：

1. 终端打印 discovery URL，例如 `https://quick.aws.com/sn?qwSurface=desktop:<nonce>:18080:1.2.4660`。
2. 在**同一台机器**的浏览器打开并完成登录。
3. 浏览器回调 `/callback`；工具按 discovery 的原始 camelCase 参数（social：`issuerUrl`/`tenantUrl`/`idcInstanceId`/`idcRegion`；enterprise：`authEndpoint`/`tokenEndpoint`/`clientId`/`clientSecret`/`tenantUrl`）拼接 authorize URL，再走 PKCE、token 交换、user-info。
4. 写入 `data/session.json`（Go 反代使用）和 `data/upstream.json`（refresh 使用）。终端只打印 email、`ws_host`、到期时间，不打印 token。

其他命令：

```bash
python auth/quickauth.py status
python auth/quickauth.py refresh    # 刷新 token 并重写 data/session.json
python auth/quickauth.py logout     # 删除 data/session.json / data/upstream.json
```

## 启动反代

```bash
./bin/quickproxy.exe -addr 127.0.0.1:8080 \
  -tokens data/tokens.json \
  -session data/session.json
```

`-session` 读取原生工具写出的 session。`data/outpost.json` 只作为兼容旧流程的可选路径；原生登录不会创建它。

常用参数：

| 参数 | 默认 | 说明 |
|---|---|---|
| `-addr` | `127.0.0.1:8080` | 监听地址 |
| `-tokens` | `data/tokens.json` | 本地 API key；文件为空/不存在时关闭鉴权 |
| `-session` | `data/session.json` | 上游 session 回退文件 |
| `-outpost` | 空 | 显式指定 outpost，如 `http://127.0.0.1:8780` |
| `-instance-token-file` | `data/instance_token` | outpost instance token |
| `-models` | `smart,fast,balanced,auto` | `/v1/models` 返回值和 model 白名单 |
| `-default-model` | `smart` | 未知 model 的映射 |
| `-timeout` | `5m` | 单请求上游超时 |
| `-keep-sessions` | `false` | 默认每轮结束后删除上游会话，避免污染聊天列表 |
| `-thinking` | 空 | `off/low/medium/high` |

## OpenAI 客户端

```bash
curl http://127.0.0.1:8080/v1/models \
  -H "Authorization: Bearer $OPENAI_API_KEY"

curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "smart",
    "stream": true,
    "messages": [{"role": "user", "content": "用三句话解释 QUIC"}]
  }'
```

Python SDK：

```python
from openai import OpenAI
# 未配置 data/tokens.json 时 api_key 可填任意值；配置后填对应 key。
client = OpenAI(base_url="http://127.0.0.1:8080/v1", api_key="local")
for chunk in client.chat.completions.create(
    model="smart",
    messages=[{"role": "user", "content": "hello"}],
    stream=True,
):
    print(chunk.choices[0].delta.content or "", end="")
```

映射规则：

- `messages` 会拼成单个 `user_message.text`；只有一条 user 消息时原样发送，多轮时按 `Role: content` 拼接。
- `model` → `agent_mode`（`smart|fast|balanced|auto`）。
- `temperature`、`max_tokens`、tools、`n>1` 不支持；`n>1` 返回 400。
- 非流式响应会等完整 `turn_completed` 后返回；流式按 `text_delta` 发 chunk。
- `X-Thread-Id` 可复用上游会话；默认每请求新建并删除。

## 安全与限制

- `data/` 内含 `session.json`（access token/refresh token）、`upstream.json`（refresh token、client secret、user info）、可选的 `tokens.json`；已 gitignore，权限尽量设为 0600。不要把 `data/` 提交或发给别人。
- 上游登录必须人工完成 IdP 登录；本工具不会、也不应该保存 IdP 密码。终端只打印 URL 和账号显示信息，不打印 token。
- **上游没有注册 API。** 网页注册入口是 `https://quick.aws.com/signup`；本工具不提供 `upstream-register`。
- 已用真实 social 账号做过端到端验证：原生登录写出 `data/session.json`，Go 反代 `/v1/chat/completions` 返回上游 `OK`。
- AIDP 路径已用真实 social 账号验证；`gateway` / `social_gateway` 的 CBOR + event-stream codec 只做了本地 round-trip，未用真实 AWS 凭证端到端验证，标记 `[UNVERIFIED]`。
- 真实上游的 `ws_path`、`provider`、字段兼容性由登录 discovery 决定；若服务端 schema 变化，先看 `/callback` 收到的参数和 `auth/quickauth.py` 的字段映射。
- 本工具仅用于授权的 CTF/沙箱分析；不要用于未授权账号或服务。
