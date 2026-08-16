# CatPaw2API

> CatPaw（美团 CatDesk）免费额度的 OpenAI 兼容代理。**无需安装/运行 CatPaw 客户端**，
> 纯 Go 直连云端 API，多账号轮转。

## 与 workbuddy2api / traework2api 的对应关系

| 能力 | workbuddy2api | traework2api | catpaw2api |
| --- | --- | --- | --- |
| 上游形态 | WorkBuddy 云端 OAuth | TRAE SOLO 云端通道 | CatPaw 云端 HTTP 通道 |
| 凭证 | auths/ 多账号 | auths/trae-*.json | auths/catpaw-*.json |
| 登录 | login.sh OAuth | login.sh 回调链接 | catpaw2api login 浏览器登录 |
| 签到/续额度 | 每日自动签到 | 每日自动签到 | - |
| 接口 | /v1/chat/completions /v1/models | 同左 | 同左 |
| 依赖 | Go | Go（零三方依赖） | Go（零第三方依赖） |
| 运维脚本 | login.sh / signin.sh / credit.sh | login.sh / signin.sh / credit.sh | login.sh / credit.sh |

## 参考项目

本项目是 [Sliverkiss](https://github.com/Sliverkiss) 同系列开源项目的延伸实现，架构与运维形态参考了以下仓库：

- [workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) — WorkBuddy CN OpenAI 兼容反代（账号池 / 轮转 / 签到架构）
- [traework2api](https://github.com/Sliverkiss/traework2api) — TRAE Work OpenAI 兼容反代（零依赖 Go 骨架）
- [qoderwork2api](https://github.com/Sliverkiss/qoderwork2api) — QoderWork CN OpenAI 兼容反代（OAuth 授权流程）

感谢原作者的开源与优秀设计。

## 快速开始（Ubuntu / Linux）

### 1. 编译（Linux 二进制）

```bash
make linux        # 产物在 bin/（纯静态，无 CGO 依赖）
make test         # 跑单测
```

### 2. 登录账号（浏览器登录，服务器上无浏览器也行）

```bash
# 服务器（无浏览器）：打印登录链接，轮询等待
./login.sh -print-only
#   ① 在任意机器浏览器打开打印的链接完成登录
#   ② 浏览器跳到打不开的 127.0.0.1 属正常，token 会自动轮询下发

# 本机有浏览器：直接打开浏览器登录
./login.sh

# 凭证落盘 auths/catpaw-{uid}.json（也可手动放同名文件）
```

### 3. 配置 & 启动

直接运行（systemd 见下）：

```bash
cp config.example.json config.json
export CP2A_API_KEY=你的随机密钥
./bin/catpaw2api -config config.json
```

### 4. 验证 + WebUI

```bash
curl http://127.0.0.1:7865/healthz
curl http://127.0.0.1:7865/v1/models -H "Authorization: Bearer $CP2A_API_KEY"
curl -X POST http://127.0.0.1:7865/v1/chat/completions \
  -H "Authorization: Bearer $CP2A_API_KEY" -H "Content-Type: application/json" \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"你好"}]}'
```

浏览器打开 **http://127.0.0.1:7865/** 即 WebUI 控制台：
账号余额/状态、对话测试（流式/非流式）。
首次使用输入 `CP2A_API_KEY` 即可（只存在当前浏览器会话）。
WebUI 是纯静态单页，无外部 CDN 依赖，可直接 iframe 嵌入网站。

多轮对话默认按账号自动续接上下文；也可用请求头
`X-Catpaw-Conversation-Id: <conversationId>` 或 body 里 `conversation_id` 显式指定会话。

## 多轮上下文与工具调用

- **完整 OpenAI 消息模型**：支持 `system / user / assistant / tool` 四种角色、
  string 与分片 `content`、assistant 的 `tool_calls`、tool 结果回传 `tool_call_id`。
- **多轮续接（指纹增量）**：网关为每个会话维护「已吸收消息前缀 + 链式指纹」，
  无状态客户端每次全量回发历史时自动做指纹比对：匹配 → 只把增量尾部发给上游
  （上下文由 CatPaw 服务端会话维持）；历史被改写 → 自动开新会话，不会串上下文。
- **工具调用（Function Calling 模拟）**：CatPaw 上游没有自定义工具通道，网关用
  「提示词注入 + ```tool_call 输出解析」模拟 OpenAI tools 语义：
  请求带 `tools` 时注入工具清单，要求模型用 ```tool_call 代码块回答；响应里
  解析出 `message.tool_calls`（`finish_reason=tool_calls`），流式按 OpenAI 增量格式
  合成输出。`tool_choice` 支持 `auto / none / required / {type:function,name}`。
- **会话标识**：响应头 `X-Catpaw-Conversation-Id` 与响应体 `conversation_id`
  返回本次会话 ID，客户端可带回继续对话；`usage` 为估算值（上游不报用量）。

## 部署（systemd / Docker）

systemd（Ubuntu）：

```bash
sudo mkdir -p /opt/catpaw2api && sudo cp -r bin config.example.json auths /opt/catpaw2api/
sudo cp deploy/catpaw2api.service /etc/systemd/system/
# 编辑 /opt/catpaw2api/.env 写入 CP2A_API_KEY，改好 config.json
sudo systemctl daemon-reload && sudo systemctl enable --now catpaw2api
sudo journalctl -u catpaw2api -f
```

Docker：

```bash
export CP2A_API_KEY=你的随机密钥
mkdir -p auths data
docker compose up -d --build
```

## 配置

`config.json` 全部项可用 `CP2A_*` env 覆盖（`CP2A_API_KEY` 只能走 env）：
`CP2A_LISTEN` / `CP2A_AUTH_DIR` / `CP2A_STATE_FILE` / `CP2A_DEFAULT_MODEL`。
详见 `.env.example`。

## 目录结构

```
cmd/server/        HTTP 服务（config + main）
cmd/login/         浏览器登录 → auths/catpaw-{uid}.json
cmd/credit/        余额查询 + 手动申请额度
cmd/apply/         批量自动申请额度（对应 signin）
internal/auth/     auth 文件读写
internal/upstream/ 云端客户端（网关/直连/聊天/SSE）+ 常量
internal/pool/     账号池（token 校验/冷却/禁用）
internal/scheduler/余额看门狗 + 自动申请额度
internal/server/   OpenAI 兼容路由
internal/webui/    内嵌 WebUI 控制台（/ 根路径）
deploy/            systemd unit 样例
docs/              逆向过程与接口清单
```

## 逆向依据（详见 docs/reverse-engineering.md）

- 网关：`https://catx.nocode.cn`（X-Auth-Token）
- 直连：`https://ai.catpaw.meituan.com`（X-Passport-Token / Cookie）
- 聊天：`https://nocode.cn/api/chat/create` + `/api/chat/agent-stream` → conversationId
- 流式：`POST /api/agent/stream/connect`（SSE）
- 模型：`GET /api/agent/maas/model-types`
- 登录：login-config → 本地回调/poll-token（token 有效期 72h）

## 免责声明

仅供学习和研究使用。使用者需遵守 CatPaw 服务条款，自行承担使用风险。

## License

MIT
