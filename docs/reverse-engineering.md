# CatPaw 逆向记录（2026-08-03 初版，2026-08-12 修正）

> 素材：`D:\CatPaw\resources\app.asar`（Electron 打包），解析 `dist-electron` /
> `dist` / `node_modules/@catx/desk-channel-sdk` / `node_modules/@catpaw-ui/*`，
> 以及积分中心网页 `credit.catpaw.meituan.com` 的产物。
> 2026-08-12 修正：直接调上游接口实测，修正了 model-types 方法、信封格式、
> SSE 通道可用性、多轮续接方式等若干逆向错误。

## 1. 架构

CatPaw 桌面端（CatDesk）是 Electron 应用，云端分为三层：

1. **Passport 认证**：`catx-passport` provider，浏览器 OIDC（loopback callback）。
2. **网关 Gateway**：`https://catx.nocode.cn`，REST 鉴权 `X-Auth-Token`。
3. **直连服务**：`https://ai.catpaw.meituan.com`，鉴权 `X-Passport-Token` + Cookie，
   SSE 流式对话。

桌面端内部还有一个 Pike（美团自研 MSI）WebSocket 通道用于消息发送；**free 计划
账号的 `/api/agent/stream/connect` SSE 端点实测不吐数据**（HTTP 200 +
`Content-Type: text/event-stream` 但 body 持续为空，30s 无字节），真实推送走 Pike
WebSocket。catpaw2api 不依赖客户端进程，因此对 free 账号改用**轮询 history** 拿回复。
NoCode 链路（`nocode.cn/api/chat/*`）提供纯 HTTP 发送方式。

## 2. 关键端点

| 用途 | 方法/路径 | Host | 鉴权 | 信封 |
| --- | --- | --- | --- | --- |
| 登录配置 | GET /api/gateway/passport/login-config | catx.nocode.cn | - | {code,message,data} |
| 轮询 token | GET /api/gateway/passport/poll-token?sid= | catx.nocode.cn | - | 同上 |
| 当前用户 | GET /api/gateway/passport/current-user | catx.nocode.cn | X-Auth-Token | 同上 |
| token 校验 | GET /api/gateway/auth/ping | catx.nocode.cn | X-Auth-Token | 同上 |
| 余额 | GET /api/gateway/credit/balance | catx.nocode.cn | X-Auth-Token | 同上（availableCredits 是字符串如 "1188.10"） |
| 注册奖励 | POST /api/gateway/credit/register {registerChannel:"CATPAW_PC"} | catx.nocode.cn | X-Auth-Token | 同上 |
| 模型表 | **POST** /api/agent/maas/model-types | ai.catpaw.meituan.com | X-Passport-Token + Cookie + user-mis-id | {unifyCode,code,msg,data,success} |
| 创建聊天 | POST /api/chat/create | nocode.cn | access-token + client-id | {code,msg,data,success} |
| 发送消息 | POST /api/chat/agent-stream | nocode.cn | access-token + client-id | 同上，data.conversationId + streamMessageId |
| 流式订阅 | POST /api/agent/stream/connect | ai.catpaw.meituan.com | X-Passport-Token + Cookie | **free 账号不吐数据**，仅作 pro 账号备选 |
| 历史 | GET /api/agent/conversation/history?conversationId=&page=1&pageSize=50 | ai.catpaw.meituan.com | X-Passport-Token + Cookie + user-mis-id | {unifyCode,code,msg,data,success} |
| 活动/邀请 | POST /api/campaign/init-after-login | credit.catpaw.meituan.com | X-Passport-Token | - |

`client-id` = `2f73f89b68`（product.json auth.provider.clientId）。

### 修正要点

- **model-types 是 POST 不是 GET**：GET 返回 405。POST body 必填 `tenant` + `scene` +
  `env`（逆向自 app.asar 的 ModelTypes 模块：`fetchModelTypes` 调
  `A("/api/agent/maas/model-types", {tenant:"CatDesk", scene:"CATX_APP", env:"EXTERNAL"})`）。
  真实值 `CatDesk`/`CATX_APP`/`EXTERNAL` 在 free 账号上返回 7 个模型：
  `LongCat-2.0`(限时免费)、`deepseek-v4-flash`、`deepseek-v4-pro`、`glm-5.2`、
  `MiniMax-M3`、`kimi-k3`、`auto`。每个模型项含 `modelTypeName`(=chat 用的 model id)、
  `catPawModelType`、`description`、`supportImage`、`modelTypeId`、`extendedInfo`
  (shortName/rateMultiplier/marketingCopy/icon)。
- **两套信封**：网关用 `{code,message,data,errorCode}`，直连/聊天用
  `{unifyCode,code,msg,data,success}`。`doJSON` 必须同时兼容（优先按 success 判定）。
- **SSE 在 free 账号不可用**：stream/connect 返回 200 但不吐数据。纯 HTTP 代理改用
  轮询 `/api/agent/conversation/history`（每 500ms，assistant 出现且 finished=true
  即完整回复，实测 3-6s）。

## 3. 登录流程（CatxPassportLoginProvider）

1. 本地监听 `127.0.0.1:0/callback`。
2. GET `login-config` 得 `loginEntryUrl`。
3. 拼 `loginEntryUrl?state=<32hex>&redirect=http://127.0.0.1:{port}/callback&sid=<32hex>`，
   浏览器打开。
4. 双通道竞争拿 token：a) 网关 POST 回调 `{token,state}`；b) 每 1s 轮询
   `poll-token?sid=`。
5. `current-user` → `{userId,userName}`。
6. token 有效期 72h（ACCESS_TOKEN_LIFETIME_MS=4320min），无 refresh_token，
   到期重新登录。

## 4. 聊天请求格式

创建：

```json
{"chatId":"desk-<rand>","source":"catdesk","prompt":"...","images":[],"files":[],
 "type":"COMMON","techStackTemplateId":"default"}
```

发送（返回 `data.conversationId` + `data.streamMessageId`）：

```json
{"chatId":"desk-<rand>","messages":[{"role":"user",
  "content":[{"type":"text","text":"..."}],"createTime":"ISO"}],
 "files":[],"urls":[],"model":"...","manualToolName":"","toolMessageId":"",
 "isAgent":true,"currentPath":"/","techStackTemplateId":"default",
 "sourcePlatform":"web","isImportProject":false,"frontCreateTime":<ms>}
```

**多轮续接**：同 chatId 再次 agent-stream → 返回**相同** conversationId、roundId+1，
上下文由服务端按 chatId 维持（实测「我叫小明」→「我叫什么」答对）。无需重放历史。
网关 `planConversation` 在客户端显式带 conversation_id 时复用 conv.ChatID 续接。

## 5. SSE 帧格式（仅 pro 账号 / Pike 通道可达时）

`data:` 行 JSON，三种形态（来自 `@catpaw-ui/catpaw-transform-util`）：

```json
{"statusUpdate":"running"}
{"headlessRemoteAgentResp":{"message":{"role":"assistant",
  "content":[{"type":"text","text":"..."},{"type":"reasoning","reasoning":"..."}],
  "finished":false}}}
{"headlessRemoteAgentResp":{"error":"..."}}
{"type":"model_chunk","content":"增量","reasoningContent":"增量","finished":false}
{"type":"agent_end","reason":"completed"}
{"type":"agent_error","error":"...","code":"..."}
```

历史回放帧带 `isHistoryMessage:true`（续会话时跳过，避免回显旧内容）。

> free 账号实测此 SSE 通道不吐数据，catpaw2api 改用轮询 history（见 §2）。

## 6. 额度/签到核对结论

- **没有每日签到接口**。
- 免费额度 = **注册奖励**：`POST /api/gateway/credit/register`，响应
  `data.registrationBonus`（新用户）、`rewardExpireDays`（默认 365）、
  `rewardLastValidDate`；重复调用返回 `newUser:false`（幂等）。
- 积分中心网页（credit.catpaw.meituan.com）：balance / transactions / plans /
  current / orders / addons / campaign init / invite。
- 未在产物中找到「余额 < 50 → 申请 +500」的动态接口；该说法大概率是注册奖励
  活动（截止 2026-08-27）的转述。catpaw2api 的 quota 调度器默认用 register
  自动领取，并保留 campaign 开关，便于实测后切换。

## 7. 脱敏

本仓库不包含任何真实 token。`auths/`、`data/`、`config.json`、`.env` 均 gitignore。

## 8. 真实模型表（已从 app.asar 提取并验证）

`POST /api/agent/maas/model-types` body `{"tenant":"CatDesk","scene":"CATX_APP","env":"EXTERNAL"}`
在 free 计划账号上返回 7 个模型（已全部实测可在 chat 里直接用 model 字段指定）：

| modelTypeName | catPawModelType | description | supportImage | rateMultiplier | 备注 |
| --- | --- | --- | --- | --- | --- |
| `LongCat-2.0` | 77 | LongCat-2.0 | true | 0.00 | 限时免费 |
| `deepseek-v4-flash` | 63 | DeepSeek-V4-Flash | false | 0.03 | 新 |
| `deepseek-v4-pro` | 64 | DeepSeek-V4-Pro | false | 0.48 | |
| `glm-5.2` | 75 | GLM-5.2 | true | 0.44 | |
| `MiniMax-M3` | 70 | MiniMax-M3 | true | 0.22 | |
| `kimi-k3` | 83 | Kimi-K3 | true | 0.94 | |
| `auto` | 0 | Auto | false | - | isAuto |

注意大小写敏感：`LongCat-2.0`、`MiniMax-M3`、`kimi-k3` 首字母大写。
`glm-4.7`/`glm-5.1`/`kimi-k2-instruct`/`minimax-m2`/`snap-chat`/`longcat2` 等
旧文档里的名字是错的，上游不接受。
