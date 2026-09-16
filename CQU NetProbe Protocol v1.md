# CQU NetProbe Protocol v1

本文档定义 `cqu-netprobe` 与 `cqu-netprobe-gateway` 之间的通信协议。

协议版本：`1`

两个项目必须严格遵守本文档。任何破坏兼容性的修改必须升级协议版本，不得单方面修改字段名称、类型、单位或语义。

---

# 1. 通信模型

通信方向：

```text
cqu-netprobe
      │
      │ HTTPS POST
      ▼
cqu-netprobe-gateway
      │
      │ /metrics
      ▼
Prometheus
```

Probe 主动向 Gateway 上报。

Gateway 不主动连接 Probe。

Probe 不直接与 Prometheus 通信。

---

# 2. Push Endpoint

固定 API：

```text
POST /api/v1/push
```

本协议另外定义了 `GET /api/v1/targets`，供探针获取可测量的 Target 列表与测量参数
（探测周期、ICMP/HTTP/DNS 的各项超时）。它同样是**增量**的：不调用它的探针行为完全不变，
因此不构成协议版本变更。详见 §32。

生产环境必须使用 HTTPS。

示例：

```text
https://netprobe.example.com/api/v1/push
```

---

# 3. HTTP Request

必须包含：

```http
POST /api/v1/push HTTP/1.1
Host: netprobe.example.com
Authorization: Bearer <TOKEN>
Content-Type: application/json
User-Agent: cqu-netprobe/<VERSION>
```

## Authorization

格式固定：

```text
Authorization: Bearer <TOKEN>
```

例如：

```text
Authorization: Bearer cqu_probe_xxxxxxxxxxxxxxxxxxxxxxxxx
```

Token 由 Gateway 生成。

Probe 不解析 Token。

Probe 只负责原样保存和发送。

Token 不允许：

- 写入日志；
- 上传到其他服务器；
- 作为 JSON 字段发送；
- 作为 Prometheus Label。

---

# 4. Content-Type

固定：

```text
application/json
```

允许：

```text
application/json; charset=utf-8
```

其他 Content-Type Gateway 返回：

```text
415 Unsupported Media Type
```

---

# 5. Request Body

v1 请求结构固定如下：

```json
{
  "version": 1,
  "timestamp": 1789490000,
  "probe_version": "0.1.0",
  "results": {
    "aliyun_dns": {
      "icmp": {
        "success": true,
        "sent": 5,
        "received": 5,
        "loss_ratio": 0.0,
        "min_rtt_ms": 10.2,
        "avg_rtt_ms": 12.3,
        "max_rtt_ms": 15.8,
        "jitter_ms": 1.4
      }
    },
    "campus_dns": {
      "dns": {
        "success": true,
        "duration_ms": 8.4
      }
    },
    "cqu_mirror": {
      "http": {
        "success": true,
        "status_code": 200,
        "duration_ms": 51.2
      }
    }
  }
}
```

---

# 6. 顶层字段

## version

类型：

```text
integer
```

v1 固定：

```json
"version": 1
```

Gateway 收到不支持的版本：

```text
400 Bad Request
```

错误代码：

```text
unsupported_version
```

---

## timestamp

类型：

```text
integer
```

单位：

```text
Unix Timestamp Seconds
```

含义：

Probe 完成本轮测量时的本地 Unix 时间。

例如：

```json
"timestamp": 1789490000
```

注意：

该时间仅作为测量辅助信息。

Gateway 判断 Probe 在线状态必须使用：

```text
server_received_at
```

即 Gateway 成功接收到合法 Push 的服务器时间。

不得使用客户端 timestamp 作为 `last_seen`。

---

## probe_version

类型：

```text
string
```

格式建议：

```text
Semantic Versioning
```

例如：

```json
"probe_version": "0.1.0"
```

最大长度：

```text
32 bytes
```

不得将 `probe_version` 作为高基数 Prometheus Label 使用。

---

## results

类型：

```text
object
```

Key 为固定 Target ID。

例如：

```text
aliyun_dns
dnspod_dns
cloudflare_dns
campus_dns
cqu_mirror
```

Target ID 必须由双方代码共同定义。

Probe 不允许自行产生新的 Target ID。

Gateway 必须维护 Target Allowlist。

---

# 7. ICMP Result

结构：

```json
{
  "icmp": {
    "success": true,
    "sent": 5,
    "received": 5,
    "loss_ratio": 0.0,
    "min_rtt_ms": 10.2,
    "avg_rtt_ms": 12.3,
    "max_rtt_ms": 15.8,
    "jitter_ms": 1.4
  }
}
```

字段定义：

```text
success       boolean
sent          integer
received      integer
loss_ratio    number
min_rtt_ms    number
avg_rtt_ms    number
max_rtt_ms    number
jitter_ms     number
```

约束：

```text
sent > 0

0 <= received <= sent

loss_ratio =
(sent - received) / sent

0 <= loss_ratio <= 1

min_rtt_ms >= 0
avg_rtt_ms >= 0
max_rtt_ms >= 0
jitter_ms >= 0
```

RTT 单位统一：

```text
milliseconds
```

### success 定义

只要本轮至少收到一个合法 ICMP Reply：

```text
success = true
```

即：

```text
received > 0
```

全部丢包：

```text
success = false
received = 0
loss_ratio = 1.0
```

---

# 8. ICMP 全部失败时的数据格式

如果：

```text
sent = 5
received = 0
```

必须：

```json
{
  "icmp": {
    "success": false,
    "sent": 5,
    "received": 0,
    "loss_ratio": 1.0,
    "min_rtt_ms": null,
    "avg_rtt_ms": null,
    "max_rtt_ms": null,
    "jitter_ms": null
  }
}
```

不得：

```text
RTT = 0
```

因为：

```text
0 ms
```

与：

```text
没有测量结果
```

语义不同。

因此无有效 RTT 时统一使用：

```text
null
```

Gateway 不为 null RTT 暴露对应 RTT Metric。

---

# 9. Jitter 定义

为了避免两个开发者实现不同算法，v1 明确定义：

假设成功收到的 RTT 为：

```text
r1, r2, ..., rn
```

jitter 定义为相邻成功 RTT 差值绝对值的平均：

```text
jitter =
(|r2-r1| + |r3-r2| + ... + |rn-r(n-1)|)
/
(n-1)
```

如果：

```text
received < 2
```

则：

```json
"jitter_ms": null
```

---

# 10. DNS Result

格式：

```json
{
  "dns": {
    "success": true,
    "duration_ms": 8.4
  }
}
```

字段：

```text
success       boolean
duration_ms   number | null
```

单位：

```text
milliseconds
```

成功：

```json
{
  "success": true,
  "duration_ms": 8.4
}
```

失败：

```json
{
  "success": false,
  "duration_ms": null
}
```

v1 不上传具体错误字符串。

避免：

- 泄露不必要信息；
- 出现任意字符串；
- 增加协议复杂度。

错误原因只记录在 Probe 本地日志。

---

# 11. HTTP Result

格式：

```json
{
  "http": {
    "success": true,
    "status_code": 200,
    "duration_ms": 51.2
  }
}
```

字段：

```text
success        boolean
status_code    integer | null
duration_ms    number | null
```

## success 定义

v1 固定定义：

```text
HTTP 200 <= status_code < 400
```

则：

```text
success = true
```

例如：

```text
200 → true
204 → true
301 → true
302 → true
404 → false
500 → false
```

如果 TCP/TLS/HTTP 请求根本没有获得 HTTP Response：

```json
{
  "success": false,
  "status_code": null,
  "duration_ms": null
}
```

如果成功获得 HTTP Response，但状态码表示失败：

```json
{
  "success": false,
  "status_code": 500,
  "duration_ms": 63.2
}
```

---

# 12. Target 定义

v1 Target 必须在双方代码中统一定义。

建议建立共同文档：

```text
docs/targets-v1.md
```

例如：

```text
ID                TYPE       TARGET
------------------------------------------------
campus_dns        ICMP/DNS   <校园 DNS>
aliyun_dns        ICMP       223.5.5.5
dnspod_dns        ICMP       119.29.29.29
cloudflare_dns    ICMP       1.1.1.1
cqu_mirror        HTTP       https://mirrors.cqu.edu.cn/
```

正式开发前双方必须共同确认实际 Target。

Target ID 一旦进入 v1 正式版本：

不得随意重命名。

例如：

```text
aliyun_dns
```

以后不能直接改成：

```text
alidns
```

否则 Prometheus 历史时序会产生新的 Series。

---

# 13. 一个 Target 可以包含多个 Probe Type

允许：

```json
"campus_dns": {
  "icmp": {
    ...
  },
  "dns": {
    ...
  }
}
```

因此：

```text
Target
 ├── ICMP
 ├── DNS
 └── HTTP
```

协议上允许多个测试类型共存。

但必须符合服务器预定义的：

```text
Target × ProbeType Allowlist
```

例如如果：

```text
cqu_mirror
```

只允许：

```text
HTTP
```

客户端发送：

```text
cqu_mirror.icmp
```

Gateway 应拒绝。

---

# 14. Gateway Response

成功固定：

```http
HTTP/1.1 204 No Content
```

无 Response Body。

Probe 收到：

```text
200 <= status < 300
```

均可以视为 Push 成功。

但 Gateway 标准实现固定返回：

```text
204
```

---

# 15. Error Response

错误统一使用 JSON：

```json
{
  "error": {
    "code": "invalid_payload",
    "message": "invalid measurement payload"
  }
}
```

Content-Type：

```text
application/json
```

错误信息不得：

- 返回 Token；
- 返回 Token Hash；
- 返回 SQL；
- 返回 Stack Trace；
- 返回服务器内部路径。

---

# 16. HTTP Status Code

双方统一：

```text
204
Push 成功

400
JSON 或测量数据不合法

401
Token 缺失、格式错误或无效

403
Probe 已被禁用

405
Method 不允许

413
Request Body 过大

415
Content-Type 不支持

429
Rate Limit

500
Gateway 内部错误

503
Gateway 暂时不可用
```

---

# 17. Error Code

v1 至少定义：

```text
invalid_request
invalid_json
invalid_payload
unsupported_version
invalid_target
invalid_probe_type
unauthorized
probe_disabled
rate_limited
internal_error
service_unavailable
```

Probe 不需要根据 `message` 判断逻辑。

程序逻辑只能依据：

HTTP Status Code

必要时再依据：

error.code

---

# 18. Retry 行为

Probe Push 失败后：

不得立即无限重试。

正常测量周期：

```text
10 seconds
```

如果 Push 失败：

保存最新一轮结果即可。

下一正常周期继续 Push。

第一版：

不补传全部历史数据。

因此：

```text
T0 测量
Push Failed

T+10 测量
Push Failed

T+20 测量
Push Success
```

Gateway 只收到 T+20 最新数据。

这是预期行为。

---

# 19. HTTP Timeout

Probe → Gateway：

推荐：

```text
connect timeout: 3s
overall request timeout: 5s
```

不得因为 Gateway 不可达阻塞下一轮程序运行。

---

# 20. Request Body Limit

v1 Gateway 最大接受：

```text
64 KiB
```

即：

```text
65536 bytes
```

超过：

```text
413 Payload Too Large
```

正常请求应该远小于该值。

---

# 21. Rate Limit

正常：

```text
1 Push / 10 seconds / Token
```

Gateway 推荐允许：

```text
burst = 3
```

建议 Token Bucket：

```text
rate = 0.2 requests/second
burst = 3
```

即长期允许：

```text
1 request / 5 seconds
```

但正常客户端仍固定约：

```text
1 request / 10 seconds
```

这样可以容忍：

- jitter；
- 网络恢复；
- 程序重启；
- 调度偏差。

HTTP 429 后 Probe 不进行特殊高速重试。

等待下一正常周期。

---

# 22. Probe 身份

Probe Request 不发送：

```text
probe_id
campus
building
network_type
```

Gateway：

```text
Authorization Token
        ↓
查询数据库
        ↓
probe_id
campus
building
network_type
```

例如数据库：

```text
TOKEN
  ↓
probe_id = hx-sy01-a83f21
campus = huxi
building = songyuan_1
network_type = wired
```

客户端对此信息没有控制权。

---

# 23. Gateway last_seen

只有满足：

```text
Token 合法
+
Probe enabled
+
JSON 合法
+
协议版本合法
+
全部 measurement validation 通过
```

Gateway 才更新：

```text
last_seen
```

收到请求 ≠ Probe 有效在线。

无效 Push 不得刷新 last_seen。

---

# 24. Prometheus 时间单位

Probe → Gateway JSON：

```text
RTT / Duration 使用 milliseconds
```

原因是方便客户端实现和 JSON 阅读。

Gateway → Prometheus：

统一转换为：

```text
seconds
```

例如：

```json
"avg_rtt_ms": 12.3
```

转换：

```text
campus_probe_icmp_rtt_seconds 0.0123
```

Prometheus Metric 遵循 Prometheus Base Units。

---

# 25. Prometheus Labels

Gateway 根据 Token 添加：

```text
probe_id
campus
building
network_type
target
```

例如：

```text
campus_probe_icmp_rtt_seconds{
  probe_id="hx-sy01-a83f21",
  campus="huxi",
  building="songyuan_1",
  network_type="wired",
  target="aliyun_dns"
} 0.0123
```

Probe 无权控制前四项。

`target` 虽然来自 Payload Key，但必须经过 Gateway Allowlist 验证。

---

# 26. 在线状态

Gateway 根据服务器自己的：

```text
last_seen
```

计算。

v1：

```text
age <= 30 seconds
→ online = 1

age > 30 seconds
→ online = 0
```

Prometheus：

```text
campus_probe_online{...} 1
```

或：

```text
campus_probe_online{...} 0
```

同时暴露：

```text
campus_probe_last_seen_timestamp_seconds{...}
```

---

# 27. Stale Measurement

v1 固定：

```text
stale threshold = 30 seconds
```

如果：

```text
now - last_seen > 30 seconds
```

Gateway：

继续暴露：

```text
campus_probe_online 0
campus_probe_last_seen_timestamp_seconds
```

但停止暴露：

```text
ICMP RTT
ICMP loss
ICMP jitter
DNS duration
HTTP duration
HTTP status
```

避免 Grafana 把旧数据误认为当前状态。

---

# 28. Gateway 不应信任的数据

以下内容全部视为不可信：

```text
timestamp
probe_version
results
target
measurement value
```

必须验证。

以下身份信息只允许来自 Gateway Database：

```text
probe_id
campus
building
network_type
enabled
```

---

# 29. 未知 JSON 字段

为了保证 v1 客户端和 Gateway 演进可控：

v1 Gateway 对未知顶层字段：

```text
忽略
```

对未知：

```text
Target
Probe Type
```

必须拒绝整个 Push：

```text
400
invalid_target

或

400
invalid_probe_type
```

这样未来可以增加可选 metadata，而不会轻易破坏兼容性。

---

# 30. 安全要求

必须：

- HTTPS；
- Bearer Token；
- Token 至少 256 bit 随机熵；
- Gateway 数据库不保存 Token 明文；
- 日志不输出 Token；
- Body ≤ 64 KiB；
- Rate Limit；
- JSON Validation；
- Target Allowlist；
- Probe Type Allowlist；
- 数值范围 Validation。

不得使用来源 IP 判断 Probe 身份。

因为校园 NAT、IPv4/IPv6、网络切换均可能改变来源地址。

---

# 31. v1 协议冻结原则

以下内容属于 v1 ABI/API：

```text
/api/v1/push

Authorization 格式

JSON 字段名称
---

# 32. Target 列表下发（`GET /api/v1/targets`）

> 本章为增量补充。不调用该端点的探针行为与本章加入前完全一致，因此协议版本仍为 `1`。

## 32.1 目的

Target 由双方代码共同定义（§12）。本章之前，探针必须把 Target 列表编译进自己的代码；
新增或调整 Target 意味着同时改两个仓库并重新部署探针。

本章让探针在运行时向 Gateway 拉取这份列表，从而只需在 Gateway 管理页面增删 Target。

Gateway 仍然是 Allowlist 的唯一权威：探针拿到什么就只能测什么，未在列表中的 Target 依旧
返回 `400 invalid_target`。**探针不得自行产生列表之外的 Target ID。**

## 32.2 请求

```http
GET /api/v1/targets HTTP/1.1
Host: netprobe.example.com
Authorization: Bearer <TOKEN>
User-Agent: cqu-netprobe/<VERSION>
```

认证方式与 §3 完全一致：同一个 Probe Token，同一套 `Authorization: Bearer <TOKEN>` 格式。

## 32.3 响应

成功固定返回：

```http
HTTP/1.1 200 OK
Content-Type: application/json
```

```json
{
  "version": 1,
  "config": {
    "interval_ms": 10000,
    "icmp": { "count": 5, "interval_ms": 200, "timeout_ms": 1000 },
    "http": {
      "method": "GET",
      "follow_redirects": true,
      "verify_tls": true,
      "timeout_ms": 5000
    },
    "dns": { "transport": "udp", "timeout_ms": 3000 }
  },
  "targets": [
    {
      "target_id": "aliyun_dns",
      "address": "223.5.5.5",
      "probe_types": ["icmp"]
    },
    {
      "target_id": "cqu_mirror",
      "address": "https://mirrors.cqu.edu.cn/",
      "probe_types": ["http"]
    }
  ]
}
```

字段：

```text
version        integer   本响应结构的版本，当前固定 1
config         object    测量参数，所有 Target 共用，见 §32.4
targets        array     可测量的 Target 列表，按 target_id 升序

target_id      string    上报时 results 的 Key（§6）
address        string    探针实际拨测的目标
probe_types    array     该 Target 允许的测量类型，升序，取值 icmp | dns | http
```

`address` 的含义由 `probe_types` 决定：

```text
icmp   要 ping 的 IP 或域名
dns    要查询的 DNS 服务器地址
http   完整 URL
```

响应中**不包含**显示名与备注：它们只用于管理页面，不参与测量，探针不应依赖。

## 32.4 测量参数（`config`）

`config` 让探针的测量方式也由 Gateway 定义，而不是编译进每个探针。本节之前，探测周期与
各项超时写在探针代码里；调整一次就要重新部署全部探针，且不同版本的探针可能用着不同的参数，
结果却混在同一个 Dashboard 上。

```text
interval_ms             integer   测量周期：相邻两轮测量的起始间隔
icmp.count              integer   每轮 Echo Request 数（即上报的 sent，§7 要求 > 0）
icmp.interval_ms        integer   两次 Echo Request 的间隔
icmp.timeout_ms         integer   单次 Echo Request 的超时，超时即计为丢包
http.method             string    请求方法，v1 固定 GET
http.follow_redirects   boolean   是否跟随 3xx 重定向
http.verify_tls         boolean   是否校验证书
http.timeout_ms         integer   整个请求的整体超时，含重定向
dns.transport           string    查询使用的传输层，v1 固定 udp
dns.timeout_ms          integer   查询的整体超时
```

v1 固定值：

```text
interval_ms       10000

icmp.count        5
icmp.interval_ms  200
icmp.timeout_ms   1000

http.method          GET
http.follow_redirects  true
http.verify_tls        true
http.timeout_ms        5000

dns.transport     udp
dns.timeout_ms    3000
```

约定：

- **本结构内所有时间一律毫秒**，与 §24 的单位规则一致。字段名带 `_ms` 后缀，因此不存在
  「秒还是毫秒」的歧义。
- 三个测量类型的分组**始终全部存在**。只测 ICMP 的探针忽略 `http` 与 `dns` 两组即可，
  不需要处理字段缺失。
- 整个 ICMP 轮次的最坏耗时为 `(icmp.count - 1) * icmp.interval_ms + icmp.timeout_ms`。
  它必须小于 `interval_ms`，否则探针会在上一轮结束前开始下一轮，两轮的丢包会被算成一轮。
  当前值：`4 * 200 + 1000 = 1800 ms`，小于 `10000 ms`。
- `http.timeout_ms` 与 §19 的 5 秒**不是同一件事**：§19 约束的是探针向 Gateway 上报时的
  HTTP 客户端超时，本字段约束的是探针测量 HTTP Target 时的超时，两者是不同的连接。
- 数值不由探针决定。探针如果实现了本地覆盖（例如调试用），上报的数据会与同组其他探针
  不可比，应避免。

参数**不区分 Target**：Gateway 不为单个 Target 定义不同的超时或周期。需要区别对待时，
应当在探针的测量类型上区分，而不是在协议里增加每 Target 参数。

## 32.5 列表内容规则

Gateway **不下发**以下 Target：

```text
enabled = 0              已禁用的 Target（§13：行为等同未知 Target）
address 为空或全为空白    无法拨测，下发只会让每一轮测量都失败
probe_types 为空          没有任何允许的测量类型
```

因此 `address` 是**运行数据**，不再是文档字段：地址未填写的 Target 不会出现在列表中。
探针实现必须能处理列表为空的情况（此时不上报任何 measurement）。

## 32.6 错误

沿用 §15 的统一错误结构，状态码沿用 §16：

```text
401  Token 缺失、格式错误或无效
403  Probe 已被禁用
405  Method 不允许
429  认证失败次数过多，来源地址被暂时限流
503  Gateway 暂时不可用
```

认证失败的限流与 §21 的按 IP 保护共用同一份额度：该端点不会成为绕过限流的 Token 猜测入口。
因此 429 既可能来自 Token 猜测被拦下，也可能来自同一出口地址（例如校园 NAT）下其他主机的
行为——探针的处理方式相同：等待下一正常周期。

## 32.7 探针侧行为

```text
启动时拉取一次
定期刷新（建议 5 分钟，具体由探针决定）
刷新失败不得阻塞测量循环：沿用上一份已知列表继续工作
```

列表顺序稳定，探针可以直接对两次响应做 diff 来决定是否重建测量计划。`config` 也在 diff
范围内：管理员调整 `config` 后（需要重新部署 Gateway），探针刷新下一次即可生效，不必重新
部署探针。

探针在任何时候都只应测量列表内、且类型被允许的 Target。列表之外的结果会被
Gateway 以 `400 invalid_target` / `400 invalid_probe_type` 拒绝（§29）。

## 32.8 同版本内的演进规则

`version` 描述的是本响应**结构**的版本，当前为 `1`。在同一版本内只允许：

```text
新增可选的字段或对象成员
```

不允许：

```text
删除字段
重命名字段
改变字段类型
改变字段单位或语义
```

探针**必须忽略不认识的字段**，不得因为出现新字段而拒绝整份响应。`config` 就是这样加进来的：
一个只认 `version` 与 `targets` 的旧探针读到本章的响应，行为与本章加入前完全一致。

只有违反上述规则时才升级 `version`。
