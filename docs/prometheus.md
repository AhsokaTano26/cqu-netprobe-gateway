# 接入 Prometheus 与 Grafana

本文讲怎么把 Gateway 的 `/metrics` 接进 Prometheus，以及怎么看懂这些指标。

指标定义在 `internal/metrics/`，跟本文一一对应。协议侧的语义在
`CQU NetProbe Protocol v1.md` 的 §24~§27。

- 抓取配置 → §1
- 指标速查表 → §2
- 「图上怎么断断续续的」→ §3
- 常用 PromQL → §4
- 告警规则（可直接用）→ §5
- Grafana 面板导入 → §6
- 容量估算与排查 → §7

---

## 1. 抓取配置

### 1.1 先确认能抓到

Gateway 有两个**独立**的 listener：

| listener | 默认地址 | 内容 |
|---|---|---|
| 公开 | `0.0.0.0:8080` | push API、管理界面、注册页 |
| 指标 | `127.0.0.1:9090` | `/metrics`，**只有这一个路由** |

在 Prometheus 所在的机器上先手工确认：

```bash
curl -s http://127.0.0.1:9090/metrics | head
```

拿到 `403` 就是 IP 白名单挡了 —— 见下一节。

### 1.2 Prometheus 与 Gateway 同机

最简单也最推荐：Gateway 的指标口只绑回环，Prometheus 从回环抓。

```yaml
# prometheus.yml
scrape_configs:
  - job_name: cqu-netprobe
    scrape_interval: 15s
    static_configs:
      - targets: ["127.0.0.1:9090"]
```

回环地址**始终**被允许，不需要配白名单。

### 1.3 Prometheus 在另一台机器

需要**同时**改三处，缺一不可：

```bash
# .env
METRICS_ADDR=0.0.0.0:9090              # 否则只绑回环，外面连不上
METRICS_ALLOWED_CIDRS=10.20.30.0/24    # 允许抓取的网段，填 Prometheus 那台机器的网段
```

```yaml
# docker-compose.yml —— 取消注释这一行
- "127.0.0.1:9090:9090"
```

宿主机侧仍是 `127.0.0.1`，由前面的 TLS 终结器 / 内网入口再转发，不要把 9090 直接暴露到公网。

**`METRICS_ALLOWED_CIDRS` 为空表示「仅回环」，不是「所有人」。** 这是刻意的失败关闭：
漏配的后果是抓不到，而不是谁都能抓。指标里带着校区、楼栋、探针 ID，属于内部拓扑信息。

> 注意 CIDR 匹配用的是**直连来源地址**。如果 Prometheus 到 Gateway 之间还有一层代理，
> 白名单看到的是代理地址 —— 这时要配 `TRUSTED_PROXY_CIDRS`（见 §1.4）。

### 1.4 前面有反向代理

如果 Gateway 前面有 nginx / Caddy / PaaS 的负载均衡，**必须**声明代理网段，否则
`/metrics` 的白名单看到的是代理的地址：

```bash
TRUSTED_PROXY_CIDRS=127.0.0.1/32,10.0.0.0/8
```

配错的表现有两种，都很难自己看出来：

- 白名单里没有代理网段 → **所有人都被 403**（包括 Prometheus）
- 把代理网段加进 `METRICS_ALLOWED_CIDRS` 但没配 `TRUSTED_PROXY_CIDRS` → 白名单**形同虚设**，
  凡是能经过代理到达的请求都被放行

判定规则与安全考量见 README §5.6。一句话：填**代理自己的**网段（不是客户端的）。

---

## 2. 指标速查表

所有时间单位都是**秒**（Prometheus 基本单位）。探针上报的毫秒值在采集时统一换算，
换算出唯一一处，见 `names.go` 的 `millisecondsToSeconds`。

### 2.1 探针测量指标

标签（除 `target` 外全部来自 Gateway 数据库，探针无权控制）：

```text
probe_id, campus, building_group, building, network_type [, target]
```

| 指标 | 类型 | 单位 | 含义 |
|---|---|---|---|
| `campus_probe_online` | gauge | 0/1 | 1 = 30 秒内成功上报过。**没有 `target`** |
| `campus_probe_last_seen_timestamp_seconds` | gauge | 秒 | 最后一次成功上报的服务器时间。**没有 `target`** |
| `campus_probe_icmp_success` | gauge | 0/1 | 本轮至少收到一个回复 |
| `campus_probe_icmp_loss_ratio` | gauge | 0~1 | 丢包率 |
| `campus_probe_icmp_rtt_seconds` | gauge | 秒 | 平均 RTT |
| `campus_probe_icmp_rtt_min_seconds` | gauge | 秒 | 最小 RTT |
| `campus_probe_icmp_rtt_max_seconds` | gauge | 秒 | 最大 RTT |
| `campus_probe_icmp_jitter_seconds` | gauge | 秒 | 相邻 RTT 差值的平均绝对值 |
| `campus_probe_dns_success` | gauge | 0/1 | 查询是否成功 |
| `campus_probe_dns_duration_seconds` | gauge | 秒 | 查询耗时 |
| `campus_probe_http_success` | gauge | 0/1 | 1 = 状态码在 200~399 |
| `campus_probe_http_duration_seconds` | gauge | 秒 | 整体耗时（含重定向） |
| `campus_probe_http_status_code` | gauge | — | 最后得到的响应码 |

> `building_group` 是 Gateway 对协议 §25 标签集的**增补**，用于把多栋楼聚合成一个园区。
> 用 `sum by (campus, building_group, target)` 就能拿到园区级的曲线。

### 2.2 Gateway 自身指标

| 指标 | 类型 | 标签 | 含义 |
|---|---|---|---|
| `cqu_netprobe_gateway_registered_probes` | gauge | — | 库里的探针总数（含禁用） |
| `cqu_netprobe_gateway_online_probes` | gauge | — | 当前在线的探针数 |
| `cqu_netprobe_gateway_push_total` | counter | — | 被接受的上报数 |
| `cqu_netprobe_gateway_push_rejected_total` | counter | `reason` | 被拒绝的上报数，按错误码分 |
| `cqu_netprobe_gateway_auth_failed_total` | counter | `reason` | 认证失败数（`invalid_token` / `missing_or_malformed`） |
| `cqu_netprobe_gateway_http_requests_total` | counter | `method, path, status` | 按**路由模式**统计，`path` 基数有界 |
| `cqu_netprobe_gateway_collect_errors_total` | counter | — | 采集失败的次数（读不到探针表） |

`reason` 是固定枚举（协议 §17 的错误码），不是任意字符串。

---

## 3. 什么时候「没有指标」，以及为什么

这一节是看图时最需要知道的：**指标消失通常是设计如此，不是采集坏了。**

| 情况 | 表现 |
|---|---|
| 探针被**禁用** | 完全不出现。禁用是管理员的刻意决定，不是故障，所以不报 `online=0` |
| 探针注册了但**从未上报** | 只有 `campus_probe_online 0`，**没有** `last_seen`（不会报 1970 那个假时间戳） |
| 探针**离线**（>30s 没上报） | `online 0` + `last_seen` 保留；**所有测量值消失** |
| 本轮 ICMP **全部丢包** | `icmp_success 0`、`loss_ratio 1`，但 RTT / jitter **不出现**（`0 ms` 与「没测到」语义不同，协议 §8） |
| 本轮 **jitter 无法计算**（回复 <2 个） | 只有 `jitter` 消失，其他 ICMP 指标还在 |
| DNS / HTTP **失败** | 只有 `success 0`，`duration` 不出现；HTTP 连响应都没拿到时 `status_code` 也不出现 |
| 某一轮**上报被拒绝** | `last_seen` 不刷新。超过 30 秒仍然是这样，探针就会翻成离线 |
| **采集失败**（读不到数据库） | 这一次抓取**什么都不输出**，同时 `collect_errors_total` +1。空白不等于探针掉了 |

所以在 Grafana 上：

- **曲线中间出现断口** = 探针离线，不是网络抖动。想看「到底断了多久」用 `online` 或
  `last_seen`，它们**不会**消失。
- **只想看趋势、不想被断口打断**，在面板里开 `Connect null values`（Grafana 的
  `spanNulls`）。

---

## 4. 常用 PromQL

```promql
# 在线探针数 / 总数
cqu_netprobe_gateway_online_probes
cqu_netprobe_gateway_registered_probes

# 某校区在线率
sum(campus_probe_online{campus="hx"}) / count(campus_probe_online{campus="hx"})

# 离线的探针（这就是要告警的集合）
campus_probe_online == 0

# 已经失联多久（秒）
time() - campus_probe_last_seen_timestamp_seconds

# 上报速率：N 个在线探针应该接近 N/10
sum(rate(cqu_netprobe_gateway_push_total[5m]))

# 全校区平均 RTT（按楼栋群聚合）
avg by (campus, building_group) (campus_probe_icmp_rtt_seconds{campus="hx"})

# 丢包最严重的 5 个目标
topk(5, avg by (probe_id, target) (campus_probe_icmp_loss_ratio))

# 跨探针的 RTT 95 分位（看整体质量，而不是单个探针）
quantile(0.95, campus_probe_icmp_rtt_seconds)

# 拒绝原因分布：持续出现的 invalid_target = 探针在测没注册的目标
sum by (reason) (rate(cqu_netprobe_gateway_push_rejected_total[5m]))

# 配置变更后没去重新拉配置的探针（config_stale 持续非零）
sum(rate(cqu_netprobe_gateway_push_rejected_total{reason="config_stale"}[10m])) > 0
```

---

## 5. 告警规则

存成 `rules/cqu-netprobe.yml`，在 `prometheus.yml` 里 `rule_files:` 引入。

```yaml
groups:
  - name: cqu-netprobe
    rules:
      # 整个 Gateway 没了。指标也一起没了，所以这条在最外层。
      - alert: NetProbeGatewayDown
        expr: up{job="cqu-netprobe"} == 0
        for: 2m
        labels: { severity: critical }
        annotations:
          summary: "Gateway 抓取失败"
          description: "Prometheus 已经 2 分钟抓不到 {{ $labels.instance }} 的 /metrics。"

      # 单个探针离线。30 秒就翻 0，用 for 压一下抖动。
      - alert: NetProbeProbeOffline
        expr: campus_probe_online == 0
        for: 2m
        labels: { severity: warning }
        annotations:
          summary: "探针 {{ $labels.probe_id }} 离线"
          description: "{{ $labels.campus }} / {{ $labels.building }}（{{ $labels.network_type }}）超过 2 分钟没有成功上报。"

      # 一个校区过半探针离线 —— 大概率是出口或汇聚的问题，不是单机
      - alert: NetProbeCampusMostlyOffline
        expr: |
          sum by (campus) (campus_probe_online)
            / count by (campus) (campus_probe_online) < 0.5
        for: 5m
        labels: { severity: critical }
        annotations:
          summary: "校区 {{ $labels.campus }} 过半探针离线"

      # 持续高丢包
      - alert: NetProbeHighPacketLoss
        expr: avg_over_time(campus_probe_icmp_loss_ratio[10m]) > 0.3
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "{{ $labels.probe_id }} → {{ $labels.target }} 持续丢包"
          description: "10 分钟平均丢包率 {{ $value | humanizePercentage }}。"

      # 探针在测没注册的目标，或用了不允许的测量类型
      - alert: NetProbePushRejected
        expr: sum by (reason) (rate(cqu_netprobe_gateway_push_rejected_total[10m])) > 0.05
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "上报被拒：{{ $labels.reason }}"
          description: "持续 10 分钟有上报被拒。invalid_target 通常是探针在测未注册的目标。"

      # 管理员改了测量参数，但探针一直没去重新拉取
      - alert: NetProbeStaleConfigNotRefetched
        expr: sum(rate(cqu_netprobe_gateway_push_rejected_total{reason="config_stale"}[10m])) > 0
        for: 10m
        labels: { severity: warning }
        annotations:
          summary: "探针没有重新拉取测量配置"
          description: "config_stale 持续了 10 分钟。探针应在收到该响应后立刻重新拉取 /api/v1/targets；若持续出现，说明探针没有实现这个逻辑。变更刚发生时短暂出现是正常的。"

      # 有人在猜 Token
      - alert: NetProbeTokenGuessing
        expr: sum(rate(cqu_netprobe_gateway_auth_failed_total{reason="invalid_token"}[5m])) > 0.5
        for: 5m
        labels: { severity: warning }
        annotations:
          summary: "大量无效 Token 尝试"
          description: "5 分钟内平均每秒 {{ $value }} 次认证失败。"

      # 采集本身在失败 —— 这段时间的图是空的，不代表探针掉了
      - alert: NetProbeCollectFailing
        expr: increase(cqu_netprobe_gateway_collect_errors_total[5m]) > 0
        for: 0m
        labels: { severity: critical }
        annotations:
          summary: "Gateway 采集失败"
          description: "读不到探针表，最近几次抓取是空的。先看 Gateway 日志里的数据库错误。"
```

---

## 6. Grafana 面板

仓库里带了一份可直接导入的面板：**`docs/grafana-dashboard.json`**。

导入方式：Grafana → Dashboards → New → **Import** → 上传该文件 → 选择 Prometheus 数据源。

面板内容：

```text
概览          在线探针 / 注册探针 / 上报接受速率 / 拒绝速率 / 采集错误
探针在线状态   表格：每个探针的在线状态与「距今多久没上报」
ICMP          RTT（平均/最小/最大）、丢包率、jitter、成功率
HTTP          响应时间、状态码、成功率
DNS           查询耗时、成功率
Gateway 自身   拒绝原因、认证失败原因、各路由响应码
```

顶部有三个筛选变量：**校区**、**探针**、**目标**，都是多选。

关于变量有个坑：它们是从**当前存在的序列**里取值的。如果所有探针都离线，测量指标全部消失，
「目标」变量就是空的 —— 这是 §3 说的设计行为在面板上的体现，不是面板坏了。
「校区」「探针」两个变量基于 `campus_probe_online`，只要探针是启用的就一直在，不受影响。

> 面板里的指标名与标签，我用一次真实抓取的输出逐条核对过（20 个指标名、全部标签），
> 没有拼写错误。但**面板 JSON 本身没有在真实 Grafana 里导入过** —— 导入后如果某个面板
> 报错，把错误贴给我。

---

## 7. 容量与排查

### 7.1 抓取间隔

Gateway 是被动接收方，抓取间隔不影响它。**15 秒**是合适的默认值：探针 10 秒一个周期，
15 秒的抓取能跟得上变化，也不会让「离线判定（30 秒）」看起来忽上忽下。

### 7.2 时序基数估算

每个探测组合（探针 × 目标 × 类型）大致产生：

```text
ICMP 目标   6 条  (success, loss_ratio, rtt, rtt_min, rtt_max, jitter)
HTTP 目标   3 条  (success, duration, status_code)
DNS  目标   2 条  (success, duration)
每个探针    2 条  (online, last_seen)
```

例：500 个探针，每个测 4 个 ICMP + 1 个 HTTP

```text
500 × (2 + 4×6 + 1×3) = 500 × 29 ≈ 14,500 条时序
```

这就是为什么要控制探针数量（`MAX_PROBES`）—— 时序数量与探针数成正比，
探针数又由公开注册页决定。

### 7.3 抓不到 / 图是空的

| 症状 | 先查这里 |
|---|---|
| `curl /metrics` 返回 403 | `METRICS_ALLOWED_CIDRS` 是否包含**直连来源**的网段；中间有代理就要配 `TRUSTED_PROXY_CIDRS` |
| 403 但白名单已配 | 是不是配了代理网段却没配 `TRUSTED_PROXY_CIDRS`？看 Gateway 日志里 `/metrics` 的请求来自哪个地址 |
| 连不上（超时） | `METRICS_ADDR` 是否还是 `127.0.0.1:9090`，以及 compose 里的端口映射有没有取消注释 |
| 部分探针没有任何指标 | 它们可能被禁用了 —— 禁用是刻意行为，不产生任何指标。去 `/admin` 看状态 |
| 所有测量指标都在，但 `online` 一直是 0 | 时钟：`online` 用服务器时间算，机器时间不对会让所有探针看起来都离线 |
| 图上有断口 | 正常。见 §3 |
