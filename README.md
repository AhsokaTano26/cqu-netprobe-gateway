# CQU NetProbe Gateway

## 1. 项目用途

本仓库是 CQU NetProbe 系统的 **中心 Gateway**：一个单实例、无外部依赖的 HTTP 服务，
接收分布在校园网各处的 Probe 主动上报的网络测量结果，并把这些结果以 Prometheus
指标的形式暴露出去，供 Grafana 看板与 Alertmanager 告警使用。

它只做三件事：

- 验证 Probe 上报的数据（Token 认证、Target allowlist、数值范围、协议版本）；
- 在内存中保存每个 Probe 的「最新一次测量」；
- 在 `/metrics` 上按 Prometheus 语义暴露在线状态与最新测量值，并自动处理 stale。

Gateway **不主动测量任何东西**，也不访问任何 Target。测量全部在 Probe 侧完成。

## 2. 系统架构

```
cqu-netprobe
      │ HTTPS POST /api/v1/push + Bearer Token
      ▼
cqu-netprobe-gateway  (单实例)
      │ GET /metrics
      ▼
Prometheus
      ├── Grafana
      └── Alertmanager
```

进程内两个相互独立的 HTTP listener：

```
0.0.0.0:8080          127.0.0.1:9090
├── /api/v1/push      └── /metrics   (IP 白名单)
└── /admin
```

- **公开 listener**（`LISTEN_ADDR`）承载 push API 与管理界面，需要放在 TLS 终结器之后。
- **指标 listener**（`METRICS_ADDR`）默认只绑定回环、只允许回环来源访问，与公开
  listener 分离。这样即使公开 listener 暴露在公网，指标也不会被一并扫到。

运行形态：单进程 + SQLite + 内存 Latest Store。无 Redis、无消息队列、无外部数据库。
测量值不落盘——SQLite 里只有 Probe、Target 与 settings 三类元数据。

## 3. Probe 与 Gateway 通信方式

Probe 定期向 Gateway 发起一次 HTTPS POST：

```http
POST /api/v1/push HTTP/1.1
Host: netprobe.example.com
Authorization: Bearer cqu_probe_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
Content-Type: application/json
```

- 成功时返回 `204 No Content`，**没有响应体**。
- 只有被接受的请求才会推进 `last_seen`（Protocol v1 §23）。被拒绝的请求不会影响在线状态。
- 判定在线使用的是 **Gateway 自己的接收时间**，而不是 Payload 里的 `timestamp`：
  Probe 的时钟不可信（Protocol v1 §23、§28）。
- 请求体上限 **64 KiB**（65536 字节，Protocol v1 §20），超限返回 `413`；
  认证失败返回 `401`；命中限流返回 `429`。
- 重试、退避与超时行为由 Probe 侧负责，见 Protocol v1 §18/§19。

管理界面（`/admin`）是给人用的，与 Probe 无关，见第 7 节。

## 4. Protocol v1

**`CQU NetProbe Protocol v1.md` 是权威文档。** Probe 与 Gateway 的行为以它为准；
本 README 只描述 Gateway 侧的部署与运维，任何冲突以协议文档为准。

Gateway 对协议的三处补充与裁决（详见设计文档）：

1. 指标上新增 `building_group` Label，位于 `campus` 与 `building` 之间，用于把多栋楼
   聚合成一个园区/楼栋群（Protocol v1 §25 的 Label 集合本身不含此项）。
2. `null` 与字段缺失等价处理。
3. Measurement 对象内部的未知字段被忽略。

**已知文档缺陷：** 当前仓库中的 `CQU NetProbe Protocol v1.md` 在 §31「v1 协议冻结原则」
处被截断——其 ABI 清单在 `JSON 字段名称` 一行后中断，代码块没有收尾。这不影响已实现
的行为，但该清单目前不完整，应补全。

## 5. 部署方法

### 5.1 准备配置

```bash
cp .env.example .env
# 编辑 .env：至少设置 PUBLIC_BASE_URL 与 ADMIN_PASSWORD（或留空让 Gateway 生成）
```

### 5.2 启动

```bash
docker compose up -d
docker compose logs -f gateway
```

镜像从 Docker Hub 拉取：`${IMAGE_REPO:-tano/cqu-netprobe-gateway}:${IMAGE_TAG:-latest}`。
也可以就地构建（`docker compose build`），或在本地直接运行二进制：

```bash
go build -o gateway ./cmd/gateway
DATA_DIR=./data LISTEN_ADDR=127.0.0.1:8080 ./gateway
```

容器以 nonroot（uid/gid 65532）、只读根文件系统、丢弃全部 capability 运行，
仅 `/data` 卷可写。

### 5.3 数据目录的属主（**原生 Linux 上必须处理**）

`./data` 以 bind mount 方式挂进容器。在原生 Linux 上它由部署用户创建，容器内的
65532 无法在其中创建 `netprobe.db`，Gateway 会以
`unable to open database file (14)` 反复崩溃重启。两种修法，任选其一：

```bash
# 方案 A：把 ./data 的属主改成容器用户
sudo chown -R 65532:65532 ./data

# 方案 B：让容器以 ./data 的属主身份运行。在 .env 中写入：
#   PUID=$(id -u)
#   PGID=$(id -g)
```

`PUID`/`PGID` 默认值均为 `65532`，与镜像内的 nonroot 用户一致。**它们由
docker-compose 读取，不是 Gateway 的环境变量。**

### 5.4 端口

- 公开 listener 的宿主机端口是 `LISTEN_HOST_PORT`（默认 `8080`），与 `LISTEN_ADDR`
  **相互独立**：`LISTEN_ADDR` 只决定容器内的绑定地址。
- 容器内 `LISTEN_ADDR` 必须保持 `0.0.0.0:8080`。published port 是转发到容器 IP 的，
  若容器内绑定 `127.0.0.1`，通过宿主机端口访问时会连不上。
- 要把服务换到别的宿主机端口，只改 `LISTEN_HOST_PORT`，不要动 `LISTEN_ADDR`。

### 5.5 暴露 metrics 端口（仅当 Prometheus 不在本机时）

docker-compose.yml 中 metrics 的端口映射默认被注释掉。取消注释前需要**同时**满足两个
条件，缺一不可：

1. `METRICS_ADDR=0.0.0.0:9090` —— 否则 metrics listener 仍只绑定容器内的回环地址，
   外面根本连不上；
2. `METRICS_ALLOWED_CIDRS` 已设置 —— 否则即使连上了，IP 白名单也只会放行回环来源，
   请求仍会被拒绝。

宿主机侧的映射保持绑定 `127.0.0.1` 即可（`- "127.0.0.1:9090:9090"`）。

### 5.6 反向代理（nginx / Caddy / PaaS）

Gateway 不终结 TLS，生产环境前面必然有东西（见 §5.7）。**只要前面有代理，就必须
配 `TRUSTED_PROXY_CIDRS`**，否则所有请求的来源地址都是代理的，四个按 IP 计的安全机制会
**静默地退化成全局一份**：

| 机制 | 退化后的表现 |
|---|---|
| Token 猜测限流（20 次/分钟） | 任何人试错 20 次 → **全部探针**在接下来一分钟收到 429 |
| 公开注册限流（3 次/小时） | 前 3 次注册用完全局的额度 → 之后**所有人**注册都 429 |
| `/metrics` IP 白名单 | 代理 IP 既非回环也不在白名单 → 403 抓不到；而把代理网段加进白名单等于**对全世界开放** |
| 日志里的 `remote_ip` | 每一条都是代理的地址，排查时没有信息量 |

配置方式：

```bash
# .env —— 填代理自己的地址/网段，不是客户端的
TRUSTED_PROXY_CIDRS=127.0.0.1/32,10.0.0.0/8
```

**判定规则**（这是安全关键，实现见 `internal/clientip`）：

- 只有当**直连方**落在 `TRUSTED_PROXY_CIDRS` 内时才采信 `X-Forwarded-For`，否则整个头被忽略
- 采信时**从右往左读**，跳过本身也在受信网段里的跳数，第一个不受信的条目就是客户端。
  从左往右读会让客户端自己选来源地址 —— 它可以在请求里预先塞一个 `X-Forwarded-For`，
  代理只会在后面追加真实地址
- 条目解析不出 IP 就跳过；全都解析不出就用直连地址

**`0.0.0.0/0` 和 `::/0` 会让 Gateway 拒绝启动。** 那不是"信任我的代理"，那是"信任任何人" ——
任何能直连到端口的人都能自称任意来源地址，上面四个机制一起失效。要信任真实网段。

nginx 侧必须**追加**而不是覆盖：

```nginx
proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
```

**没配这个变量时行为与以前完全一致**（忽略该头，用直连地址）—— 直连部署不需要改动。

### 5.7 TLS

Gateway **不做 TLS 终结**，也不做 HTTP 跳转。生产环境必须在它前面放 TLS 终结器
（Nginx / Caddy / 云负载均衡），并把外部 HTTPS 地址写进 `PUBLIC_BASE_URL`，否则：

- 创建 Probe 后展示的 Push Endpoint 会是错的；
- 管理界面的 Session Cookie 不会带上 `Secure` 标志。

### 5.8 发布镜像（维护者）

镜像发布到 **Docker Hub**，由 `.github/workflows/release.yml` 在推送 tag 时执行：

```bash
git tag v0.2.0
git push origin v0.2.0
```

一次 tag 会推送四个 tag —— `v0.2.0`、`0.2.0`、`0.2`、`latest` —— 并同时构建
`linux/amd64` 与 `linux/arm64`。

需要在仓库的 **Settings → Secrets and variables → Actions** 里配置两个 secret：

| Secret | 值 |
|---|---|
| `DOCKERHUB_USERNAME` | Docker Hub 用户名（小写），也是镜像的命名空间 |
| `DOCKERHUB_TOKEN` | Docker Hub **Access Token**（Account Settings → Personal access tokens），权限选 Read & Write。**不是账号密码** |

若你的 Docker Hub 账号不允许自动建仓库，先手动建一个 `cqu-netprobe-gateway`。
workflow 里镜像名写死为 `<DOCKERHUB_USERNAME>/cqu-netprobe-gateway`；fork 后改了仓库名
的话，改 `release.yml` 里 `metadata-action` 的那一行。

CI（`.github/workflows/ci.yml`）在每次 push 与 PR 上跑 gofmt / vet / `test -race` /
build，与发布互相独立——发布**不会**等 CI 通过，打 tag 前请确认 main 是绿的。

## 6. 环境变量

以下变量由 `config.Load` 读取（启动时校验，非法值直接拒绝启动而非静默回退）：

| 变量 | 默认值 | 含义 |
|---|---|---|
| `LISTEN_ADDR` | `0.0.0.0:8080` | 公开 listener（push API + 管理界面）的绑定地址。容器内必须绑 `0.0.0.0` |
| `METRICS_ADDR` | `127.0.0.1:9090` | `/metrics` listener 的绑定地址。默认只绑回环 |
| `DATA_DIR` | `/data` | SQLite 数据库所在目录，库文件固定为 `<DATA_DIR>/netprobe.db` |
| `PUBLIC_BASE_URL` | 空 | 对外访问地址。用于生成 Push Endpoint 展示，并决定 Session Cookie 是否加 `Secure`（值为 https 时加） |
| `ADMIN_USERNAME` | `admin` | 管理界面用户名 |
| `ADMIN_PASSWORD` | 空 | 管理员密码。**留空则启动时生成一个随机密码并在日志中打印一次**，见第 11 节 |
| `ONLINE_THRESHOLD` | `30s` | 在线判定与测量值 stale 判定共用的阈值。**改动即偏离 Protocol v1 §26/§27**，见下 |
| `RATE_LIMIT` | `5s` | 每个 Probe 的请求最小间隔（令牌桶补充速率），Protocol v1 §21 推荐值 |
| `RATE_LIMIT_BURST` | `3` | 每个 Probe 允许的突发请求数，必须 ≥ 1 |
| `REGISTER_LIMIT` | `1h` | 公开注册页每 IP 的最小注册间隔，必须为正 |
| `REGISTER_LIMIT_BURST` | `3` | 公开注册页每 IP 允许的突发注册数，必须 ≥ 1 |
| `MAX_PROBES` | `500` | 公开注册页最多能创建多少探针，必须 ≥ 1。管理员创建不受此限 |
| `TRUSTED_PROXY_CIDRS` | 空 | 其 `X-Forwarded-For` 可被采信的网段，即你的反向代理。**空 = 忽略该头，用直连地址**。见 5.6。填 `0.0.0.0/0` 会拒绝启动 |
| `METRICS_ALLOWED_CIDRS` | 空 | 除回环外允许抓取 `/metrics` 的网段，逗号分隔。**空 = 仅回环**（失败关闭，而非放开） |
| `LOG_LEVEL` | `info` | 日志级别，取值 `debug` \| `info` \| `warn` \| `error` |

以下变量**不由 Gateway 读取**，仅由 `docker-compose.yml` 做变量替换：

| 变量 | 默认值 | 含义 |
|---|---|---|
| `LISTEN_HOST_PORT` | `8080` | 公开 listener 发布到宿主机的端口 |
| `PUID` / `PGID` | `65532` / `65532` | 容器进程的 uid:gid。原生 Linux 上见 5.3 |
| `IMAGE_REPO` | `tano/cqu-netprobe-gateway` | 镜像仓库，换镜像源或 fork 时覆盖 |
| `IMAGE_TAG` | `latest` | 镜像 tag，也作为构建时的 `VERSION` |

> **`ONLINE_THRESHOLD` 警告：** Protocol v1 §26 把在线判定固定为 `age <= 30s`，
> §27 把 stale 阈值固定为 `30s`。Gateway 出于工程需要把它做成了可配置项，默认值即
> `30s`，与协议一致。**一旦改动它，就同时偏离协议并对在线判定和 stale 判定产生
> 双重影响**：`online` 会提前或延后翻转，RTT/loss/DNS/HTTP 测量值也会随之提前或延后
> 停止暴露。如需保持协议一致性，请不要修改此项。该值必须为正数。

## 7. Probe 创建与 Token 使用

有两条创建路径，**都由 Gateway 签发 Token，身份信息都由 Gateway 的数据库决定**：
管理员在管理页创建，或访客在公开页自助注册。两者都是**创建即可用**，区别只在于公开
注册受 `MAX_PROBES` 总数上限约束（见 §7.2）。

### 7.0 先建立校区与楼栋目录

两条路径都从目录里选位置，所以**先配置目录**：

- `/admin/campuses` —— 校区。`code`（如 `hx`）会作为 Prometheus 的 `campus` Label
  并进入 `probe_id`，**创建后不可修改**：改名会断裂历史 Series。显示名（如「虎溪」）
  只用于页面，随时可改。
- `/admin/buildings` —— 楼栋。同理 `code`（如 `sy01`）不可改。每个楼栋带一个楼栋群
  （如「松园」），它会作为 `building_group` Label —— **请从已有分组中选择**，手打一个
  新值会造出无法合并的 Series。

删除被探针引用的校区或楼栋会被**拒绝**（409），不会级联删除；请先迁移或删除相关探针。

### 7.1 管理员创建

1. 用管理员账号登录 `/admin/login`。
2. 打开 `/admin/probes/new`，从下拉菜单选择校区与楼栋，填写网络类型与描述。
3. 提交后，Gateway 生成 Token 并 `303` 重定向到 `/token/{slot}` 展示页面。

### 7.2 公开自助注册

访客直接访问 `GET /`（**无需登录**），选择校区、楼栋、网络类型并提交，即可拿到 Token。

**自助注册的探针创建立即可用**，没有审批环节：拿到 Token 配到探针上，它就能上报，
下一次 scrape 就会出现在 `/metrics` 里。

因为公开页是匿名的，代价是必须用配额而不是审批来兜底：

- **总数上限**：`MAX_PROBES`（默认 500）。达到上限后自助注册返回 `503` 并提示联系网络
  中心。上限**不适用于管理员创建**——表满了更不能挡住唯一能清空它的人。
- **按 IP 限流**：`REGISTER_LIMIT` / `REGISTER_LIMIT_BURST`，超限返回 `429`。

两者管的是不同的事：限流管「一个地址能多快创建」，上限管「总共能存在多少」。只靠限流
挡不住校园 NAT 下随时间累积的注册量，而每条探针都是一组 Prometheus 时序。

管理员创建与自助注册的探针除了「创建方式」之外没有区别，都可以在列表上禁用或删除。
列表上方可按 全部 / 在线 / 离线 / 已禁用 筛选。

`POST /` 是 Gateway 上**唯一一条未认证的状态变更路由**，因此：

- 按 IP 限流（`REGISTER_LIMIT` / `REGISTER_LIMIT_BURST`），超限返回 `429`；
- 请求带 `Origin` 时校验其 host 与 `PUBLIC_BASE_URL` 一致，不符返回 `403`；
- 提交的校区必须存在，且楼栋必须确实属于该校区，否则 `400`；
- 目录为空时返回 `503` 并提示联系管理员，而不是 `400`。

> 来源 IP **只用于限流，绝不用于判断探针身份**（Protocol v1 §30）。校园出口大量共享
> NAT，用来源地址判断身份会在网络切换或 NAT 变化时产生错误结论。

### 7.3 Token 的展示与保管

**Token 只显示这一次。** 它由 32 字节 `crypto/rand` 生成，形如
`cqu_probe_<43 个 base64url 字符>`（Protocol v1 §30 要求 ≥ 256 bit 熵）。

- 展示页是一次性槽：渲染后立即销毁，刷新、后退或重放都拿不到第二次；槽还有 60 秒
  TTL，超时同样失效。
- 数据库里只存 Token 的 **SHA-256 十六进制哈希**，不存明文、不可反推。明文在任何
  时候都不会写入日志。
- 因此**忘记 Token 只能轮换，不能找回**：在 `/admin/probes/{id}` 上点「轮换 Token」，
  旧 Token 立即失效，新 Token 同样只显示一次。

展示页上的 Probe ID、Token 与 Push Endpoint 都是**点击即复制**的（整行可点，复制内容
与页面显示完全一致），页脚有「返回」按钮：自助注册回到注册表单，管理员创建回到 Probe
列表，轮换回到该 Probe 详情页。

把展示页上的 Token 与 Push Endpoint 配置到 Probe 端（Endpoint 形如
`<PUBLIC_BASE_URL>/api/v1/push`），然后让 Probe 发出第一次上报。

`/admin` 的 Probe 列表展示每个 Probe 的 Online / Offline / Disabled、Last Seen 与
身份信息。禁用（toggle）一个 Probe 后，它的 push 会返回 `403 probe_disabled`，且
`/metrics` 上不再暴露它的任何 Series——禁用是管理员的刻意决定，与「掉线」不是一回事。

### 7.4 探针获取 Target 列表

`GET /api/v1/targets`（需要与 push 相同的 `Authorization: Bearer <TOKEN>`）：

```bash
curl -H "Authorization: Bearer cqu_probe_xxx" https://netprobe.example.com/api/v1/targets
```

```json
{
  "version": 1,
  "config": {
    "interval_ms": 10000,
    "icmp": { "count": 5, "interval_ms": 200, "timeout_ms": 1000 },
    "http": { "method": "GET", "follow_redirects": true, "verify_tls": true, "timeout_ms": 5000 },
    "dns": { "transport": "udp", "timeout_ms": 3000 }
  },
  "targets": [
    {"target_id": "aliyun_dns", "address": "223.5.5.5", "probe_types": ["icmp"]},
    {"target_id": "cqu_mirror", "address": "https://mirrors.cqu.edu.cn/", "probe_types": ["http"]}
  ]
}
```

`address` 的含义由 `probe_types` 决定：`icmp` 是要 ping 的 IP/域名，`dns` 是要查询的 DNS
服务器地址，`http` 是完整 URL。

`config` 是**测量参数**：探测周期、ICMP 每轮次数与超时、HTTP 重定向/证书校验/超时、
DNS 超时（另有 `http.method`、`dns.transport` 两项协议固定值）。它让这批参数由 Gateway
统一定义，而不是编译进每个探针——在 `/admin/settings` 改一次即对所有探针生效（探针下次
刷新时），不必重新部署探针。详见 §7.5。

**不下发的 Target：** 已禁用的、地址为空的、以及没有任何允许探测类型的。因此 `address`
是运行数据而不是备注——**地址没填的 Target 探针根本看不到**，管理页面上会以红色标出。

响应按 `target_id` 稳定排序，探针可以直接 diff 两次响应来决定是否重建测量计划（`config`
也在 diff 范围内）。响应里**不含**显示名与备注，探针不应依赖它们。

该端点复用 push 的认证与认证失败限流；探针侧建议启动拉取一次、之后每 5 分钟刷新，刷新
失败时沿用上一份列表继续工作（不要阻塞测量循环）。

> 这是协议的**增量**补充（协议 §32）：不调用它的探针行为完全不变，因此协议版本仍是 `1`。

> 推送与目标分发两个端点的机器可读描述见 `docs/openapi.json`（§12）。

### 7.5 测量参数（管理页面）

`/admin/settings` 维护下发给**所有**探针的测量参数：探测周期、ICMP 每轮次数/间隔/超时、
HTTP 跟随重定向/校验证书/整体超时、DNS 整体超时。保存后探针在下次刷新（建议 5 分钟）时
生效，不需要重新部署探针，也不需要重启 Gateway。

两项**不可修改**，页面上以说明文字而非输入框呈现：`http.method` 固定 `GET`、
`dns.transport` 固定 `udp`。它们决定测量结果的语义，改了会让这组探针的数据与其他探针
不可比，因此属于协议 v1 的固定值。

保存时校验，规则见协议 §32.4，其中一条是**本机特有的**：

- **测量周期不得小于 `RATE_LIMIT`。** 周期比推送限流的补充间隔还短，探针的持续推送速率
  就超过了自己的令牌桶，突发额度用完后会被**持续限流**——表现为「半个机房的探针都坏了」，
  而配置看起来完全正常。

「恢复默认值」删除自定义值，回到协议默认：周期 10s、ICMP 5 次/200ms/1s、HTTP 5s、DNS 3s。
默认值在数据库里**没有对应的行**——没设置过就读协议默认，所以这一功能不产生数据迁移，
升级也不会因为缺少配置行而失效。

## 8. Prometheus scrape 配置

Prometheus 与 Gateway 同机时，直接抓本机回环即可：

```yaml
scrape_configs:
  - job_name: cqu-netprobe
    scrape_interval: 15s
    static_configs:
      - targets: ["127.0.0.1:9090"]
```

要点：

- `scrape_interval` 建议不超过 `15s`，它决定了看板的时间分辨率。
- 若 Prometheus 不在同一台机器/同一个 network namespace，需要先按 5.5 同时放开
  `METRICS_ADDR` 与 `METRICS_ALLOWED_CIDRS`。
- 白名单匹配的是**直连 peer 地址**，`X-Forwarded-For` 等转发头被刻意忽略。因此若
  Prometheus 经反向代理抓取，白名单里要写代理的地址。
- `/metrics` 未配置任何认证，安全性完全依赖网络层。不要把它暴露到公网。

## 9. `/metrics` 指标说明

Probe 上报的 RTT 与 Duration 是**毫秒**；暴露给 Prometheus 时统一除以 1000 转为
**秒**（Protocol v1 §24）。这个换算只发生在 `internal/metrics` 一处。

身份 Label `probe_id`、`campus`、`building_group`、`building`、`network_type`
由 Gateway 从自己的数据库填写，Probe 无权影响（Protocol v1 §22、§25）。测量类指标
在此基础上追加 `target`；`target` 虽来自 Payload，但已验证过 allowlist。

### 9.1 Probe 测量指标

| 指标名 | 含义 | 单位 | Labels |
|---|---|---|---|
| `campus_probe_online` | 该 Probe 是否在线：在上报后 `ONLINE_THRESHOLD` 内为 `1`，否则 `0` | 0/1 | 身份 Label |
| `campus_probe_last_seen_timestamp_seconds` | 最后一次**被接受**的 push 的 Gateway 接收时刻（Unix 秒） | 秒（Unix 时间戳） | 身份 Label |
| `campus_probe_icmp_success` | 该 Target 的 ICMP 探测是否成功 | 0/1 | 身份 Label + `target` |
| `campus_probe_icmp_loss_ratio` | ICMP 丢包率 | 比率 0–1（无量纲） | 身份 Label + `target` |
| `campus_probe_icmp_rtt_seconds` | ICMP 平均 RTT（由 `avg_rtt_ms` 换算） | 秒 | 身份 Label + `target` |
| `campus_probe_icmp_rtt_min_seconds` | ICMP 最小 RTT（由 `min_rtt_ms` 换算） | 秒 | 身份 Label + `target` |
| `campus_probe_icmp_rtt_max_seconds` | ICMP 最大 RTT（由 `max_rtt_ms` 换算） | 秒 | 身份 Label + `target` |
| `campus_probe_icmp_jitter_seconds` | ICMP 抖动（由 `jitter_ms` 换算） | 秒 | 身份 Label + `target` |
| `campus_probe_dns_success` | DNS 解析是否成功 | 0/1 | 身份 Label + `target` |
| `campus_probe_dns_duration_seconds` | DNS 解析耗时（由 `duration_ms` 换算） | 秒 | 身份 Label + `target` |
| `campus_probe_http_success` | HTTP 探测是否成功 | 0/1 | 身份 Label + `target` |
| `campus_probe_http_duration_seconds` | HTTP 请求耗时（由 `duration_ms` 换算） | 秒 | 身份 Label + `target` |
| `campus_probe_http_status_code` | HTTP 响应状态码 | HTTP 状态码（无量纲） | 身份 Label + `target` |

### 9.2 Gateway 自身指标

| 指标名 | 含义 | 单位 | Labels |
|---|---|---|---|
| `cqu_netprobe_gateway_registered_probes` | 已注册 Probe 总数（含已禁用） | 个 | 无 |
| `cqu_netprobe_gateway_online_probes` | 当前在线 Probe 数（不含已禁用） | 个 | 无 |
| `cqu_netprobe_gateway_push_total` | 被接受的 push 请求数 | 次（Counter） | 无 |
| `cqu_netprobe_gateway_push_rejected_total` | 被拒绝的 push 请求数，按原因分类 | 次（Counter） | `reason` |
| `cqu_netprobe_gateway_auth_failed_total` | 认证失败次数，按原因分类 | 次（Counter） | `reason` |
| `cqu_netprobe_gateway_http_requests_total` | HTTP 请求数，按方法/路由模式/状态码分类 | 次（Counter） | `method`、`path`、`status` |
| `cqu_netprobe_gateway_collect_errors_total` | scrape 时读取 Probe 表失败的次数 | 次（Counter） | 无 |

关于 `reason` Label：它是**有限枚举**，取 Protocol v1 §17 的错误码
（`invalid_request`、`invalid_json`、`invalid_payload`、`unsupported_version`、
`invalid_target`、`invalid_probe_type`、`probe_disabled`、`rate_limited`、
`internal_error` 等）；`unauthorized` 不在这里，它计入 `auth_failed_total`，取值为
`missing_or_malformed`、`invalid_token`、`rate_limited`。
原始错误字符串永远不会作为 Label 值。

关于 `http_requests_total` 的 `path`：使用**路由模式**而非真实 URL，避免公网扫描器
请求 `/wp-admin`、`/.env` 之类的路径造出大量 Series 导致基数爆炸。目前该中间件只挂在
`POST /api/v1/push` 上，因此实际只会出现 `path="/api/v1/push"` 的序列。

### 9.3 Emit 规则（什么情况下不出指标）

| Probe 状态 | 暴露内容 |
|---|---|
| 已禁用 | **什么都不暴露**，Series 自然 stale |
| 已启用但从未 push | `campus_probe_online 0`，**不暴露** `last_seen`（避免 1970 的假时间戳） |
| `age <= ONLINE_THRESHOLD` | `online 1` + `last_seen` + 全部测量指标 |
| `age > ONLINE_THRESHOLD` | `online 0` + `last_seen`，测量指标全部停止暴露 |

边界：`age == 阈值` 视为在线（与 Protocol v1 §26 的 `age <= 30` 一致）。

`null` 的语义是「没有测到」，与数值 `0` 不同，因此对应 Series 直接不出现：

- ICMP `received = 0` → `icmp_success 0`、`icmp_loss_ratio 1`，四个 RTT 系列全部不暴露；
- DNS 失败 → `dns_success 0`，`duration` 不暴露；
- HTTP 无响应 → `http_success 0`，`status_code` 与 `duration` 都不暴露；
- HTTP 有响应但状态码 ≥ 400 → `http_success 0`，`status_code` 与 `duration` 照常暴露。

所有指标都在 **scrape 时现算**，不维护 GaugeVec。这正是 stale 规则天然正确的原因。

## 10. SQLite 备份方式

数据库默认是 `data/netprobe.db`（宿主机的 `./data` 卷）。它启用了 **WAL 模式**
（`journal_mode=WAL`、`synchronous=NORMAL`、`busy_timeout=5000`）。

因为开了 WAL，容器运行期间直接 `cp netprobe.db` 拿到的不是一致快照——已提交但尚未
checkpoint 的数据还在 `-wal` 文件里。**必须用 SQLite 自己的 backup API，或者先停容器。**

```bash
# 方式一（推荐）：在线备份，容器保持运行。在宿主机上执行，需要宿主机装了 sqlite3 CLI。
# .backup 走 SQLite backup API，产出的是一个一致的、非 WAL 的单文件快照。
sqlite3 data/netprobe.db ".backup 'backup-$(date +%F).db'"

# 方式二：停容器后复制整个数据目录（最省事，也最不容易漏掉 -wal / -shm）。
docker compose stop gateway
cp -a data "backup-$(date +%F)"
docker compose start gateway
```

注意：镜像基于 distroless，**容器内没有 shell 也没有 `sqlite3` 命令**，所以
`docker compose exec gateway sqlite3 ...` 是行不通的，备份请在宿主机侧完成。

恢复：

```bash
docker compose stop gateway
rm -f data/netprobe.db data/netprobe.db-wal data/netprobe.db-shm
cp -a "backup-2026-09-16/netprobe.db" data/netprobe.db
sudo chown 65532:65532 data/netprobe.db   # 或者改用 PUID/PGID，见 5.3
docker compose start gateway
```

恢复时务必删掉残留的 `-wal` / `-shm`：它们是**旧库**的 WAL，与新拷进来的
`netprobe.db` 配不上，SQLite 会拒绝打开或报出损坏。

数据库里只有 Probe、Target 与 settings 三类元数据，体积很小（KB 级），备份成本极低；
但 Probe 的 Token 哈希在里面，**备份含有凭据材料，请按凭据对待**（不要提交进 git，
`.gitignore` 已经忽略 `*.db`）。

## 11. 安全注意事项

- **生产环境必须使用 HTTPS。** Gateway 不终结 TLS，也不做跳转，必须由前置的
  TLS 终结器承担。Protocol v1 §2 同样要求生产环境使用 HTTPS。
- **`/metrics` 默认只对回环开放**，并且默认只绑定回环地址，形成双重保护。
  只有同时设置 `METRICS_ADDR` 与 `METRICS_ALLOWED_CIDRS` 才会对外放开；
  它没有任何认证机制，网络层是唯一防线，不要直接对公网暴露。
- **管理员凭据不得使用默认值。** 请显式设置 `ADMIN_PASSWORD`，或接受 Gateway 生成的
  随机密码（32 字节 `crypto/rand`，43 字符 base64url）。密码使用 bcrypt 校验
  （常数时间比较）。
- **Probe Token 只显示一次，且只以 SHA-256 哈希存储。** 明文不落盘、不进日志。
  Token 是 256 bit 均匀随机值，因此用 SHA-256 而非 bcrypt 是刻意的：没有字典空间
  需要慢哈希去防，而慢哈希会让每次 push 都要全表扫描加 N 次 KDF。
- **Token 永远不会写入日志。** 日志里出现的只有公开的 `probe_id`，不是 Token 或它的哈希。
- **Gateway 不信任任何客户端提供的数据。** 身份 Label 全部取自数据库；判定在线用的是
  Gateway 自己的接收时间；`target` 必须通过 allowlist；来源 IP 绝不用于判定 Probe 身份
  （校园 NAT、IPv4/IPv6 双栈、网络切换都会改变源地址——Protocol v1 §28）。
- **转发头被忽略。** 按 IP 的限流与 `/metrics` 白名单都基于直连 peer 地址，
  `X-Forwarded-For` 是客户端可控的，不会被采信。
- **已知取舍：登录失败没有限速。** 默认路径下密码是 32 字节随机值，暴力破解不可行；
  但若管理员自行把 `ADMIN_PASSWORD` 设为弱密码，`/admin/login` 存在被爆破的风险。
  这是有意的 v1 取舍，不是遗漏。另请注意：按 IP 的认证失败限速**只作用于 Probe 的
  push 认证失败路径**，不覆盖管理界面登录。

### 忘记管理员密码怎么办

`ADMIN_PASSWORD` 留空时，Gateway 生成的密码会在**首次启动时打印一次**，此后不再打印；
存入 `settings` 表的只是 bcrypt 哈希，所以密码本身能跨重启继续使用。要重置：

```bash
sqlite3 data/netprobe.db "DELETE FROM settings WHERE key='admin_password_hash';"
docker compose restart gateway
# 新密码会在容器日志中打印一次
docker compose logs gateway | grep -i password
```

> 若 `ADMIN_PASSWORD` 已显式设置，则以环境变量为准，每次启动都会用它重新哈希，不需要
> 重置流程。

## 12. 相关文档

| 文件 | 内容 |
|---|---|
| `CQU NetProbe Protocol v1.md` | **权威**。Probe 与 Gateway 之间的协议定义 |
| `docs/openapi.json` | 接口的 OpenAPI **3.1** 描述，见下 |
| `docs/targets-v1.md` | Protocol v1 §12 要求的双方共同 Target 定义 |
| `.env.example` | 全部环境变量及其注释 |
| `docs/superpowers/specs/` | 设计文档（内部） |

`docs/openapi.json` 可直接导入 Swagger UI、Postman 或用于生成客户端。协议文档是权威定义，
它是协议文档的机器可读表达，两者冲突时以协议文档为准。

它覆盖 `/api/v1/push`、`/api/v1/targets` 与 `/metrics`；管理页面与自助注册页面是 HTML
表单界面，不属于它的范围。

文件是手写的，所以由测试守着：`internal/api/openapi_test.go` 与 `cmd/gateway/main_test.go`
会真的发请求，断言每个状态码、每个字段名、每个 `config` 数值都与网关实际行为一致。任何
一边改了而另一边没跟上，测试就会失败——这是这份文档不会随时间变成谎言的唯一原因。
