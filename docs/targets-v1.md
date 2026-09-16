# Targets v1

Protocol v1 §12 要求双方在开发前共同确认 Target 列表，并禁止在 v1 中重命名已在用的
Target ID：重命名会产生新的 Prometheus Series，并把历史时序孤立在旧 ID 上。

本列表以 Gateway 为准。它存放在 SQLite 中，由 `/admin/targets` 页面维护。首次启动时若
`targets` 表为空，Gateway 会自动播种下表。

| Target ID | Probe Types | Address | 状态 |
|---|---|---|---|
| `campus_dns` | icmp, dns | **TODO — 待填写** | 播种时无地址，因此**不下发**给探针 |
| `aliyun_dns` | icmp | 223.5.5.5 | 已播种 |
| `dnspod_dns` | icmp | 119.29.29.29 | 已播种 |
| `cloudflare_dns` | icmp | 1.1.1.1 | 已播种 |
| `cqu_mirror` | http | https://mirrors.cqu.edu.cn/ | 已播种 |

## 待办事项

`campus_dns` 尚未确认实际地址。校园 DNS 服务器确定后，请在 `/admin/targets` 补填。
Gateway 自身从不访问任何 Target，但**地址为空或全为空白的 Target 不会下发给探针**——
在补填之前，探针既看不到它，也不会测量它。

## 探针如何拿到这份列表

探针在运行时通过 `GET /api/v1/targets` 拉取本表（协议 §32），地址与允许的测量类型都在
响应里，不必编译进探针。响应同时包含 `config`：探测周期与 ICMP／HTTP／DNS 的各项参数，
同样是 Gateway 统一定义、探针拉取生效（协议 §32.4）。

因此本文件的角色是**记录双方的共同约定**，而不是探针的配置来源。探针的实际行为以下发的
响应为准；本文与之下不一致时，以 Gateway 下发的为准，并应当尽快修正本文。

## 播种语义

播种的守卫条件是 `SELECT COUNT(*) FROM targets` 为 0，即「表为空时执行」，而不是
「只执行一次」。由此产生两个后果，务必知悉：

- 删除单个 Target 是永久性的，不会被重新播种。
- 若把全部 Target 删光，下一次启动会重新播种完整的默认集合。

五个默认 Target 在同一事务中写入，中途失败则整体不提交。

## 规则

- 不在本列表中的 Target ID 会被拒绝，返回 `400 invalid_target`。
- Target 未允许的 Probe Type 会被拒绝，返回 `400 invalid_probe_type`。
- 禁用（`enabled = 0`）一个 Target 会把它从 allowlist 中移除，因此在重新启用之前，
  它的行为与未知 Target 完全一致：同样是 `400 invalid_target`。
- 不要重命名 Target ID。请新增一个 ID，并把 Probe 迁移过去。
