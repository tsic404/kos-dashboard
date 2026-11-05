# kos-dashboard

KOS 桌面项目：浏览器桌面与 BFF — 桌面、窗口、dock、launcher、应用宿主、通知中心、主题、全局搜索。

- 设计：`tsic/kos-design` 仓的 `kos-dashboard/`（范围、契约、验收、决策记录）
- 设计依据：`tsic/kos-design@05bf60a`

## 边界

- 桌面条目、桌面页、窗口布局与 dock/launcher 偏好由本项目贡献。
- BFF 只做代理、聚合与静态资产分发，不复制业务规则，不持有集群凭据。
- 窗口交互状态留在浏览器会话；布局进用户偏好；应用内部状态归应用。

## 现有实现

| 包 | 职责 |
|---|---|
| `pkg/dock` | 桌面条目对象类（DASH-C-30）、桌面页声明（DASH-C-52）、dock 偏好域（DASH-C-27） |
| `pkg/desktop` | 桌面页投影：条目、已安装应用、最近使用与通知中心，逐项带来源（KOSD-C-53） |
| `pkg/bff` | 会话引导与登录代理、控制面 API 代理与错误包络、事件 SSE、CSRF/CSP、静态资产 |
| `pkg/bff/assets/shell` | 浏览器外壳：启动、路由、错误边界、页面注册表导航、桌面页渲染、事件流 |
| `cmd/kos-dashboard` | BFF 进程入口 |

外壳是原生的 ES 模块，随包分发、无构建步骤、无运行时依赖：

| 模块 | 职责 |
|---|---|
| `api.js` | 控制面 API 客户端；错误包络解码与 CSRF 双提交 |
| `registry.js` | 页面注册表：入口列表、页面读取地址、默认路由 |
| `render.js` | 纯渲染函数：导航、页面、四类展示项、通知已读状态、只读与陈旧标记、按角色裁剪动作入口、错误包络 |
| `recent.js` | 本地源：浏览器存储中的最近使用 |
| `stream.js` | 事件流订阅；断流时保留上次观测并标陈旧 |
| `app.js` | 外壳状态：会话、注册表、当前页面、动作提交（写入体原样带上）、事件流刷新 |
| `main.js` | 浏览器入口：把外壳接到文档与事件上 |

## 构建与运行

```sh
make build
KOS_DASHBOARD_SESSION_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '\n') \
  go run ./cmd/kos-dashboard -address :8080 -upstream http://127.0.0.1:8081
```

| 参数 | 语义 |
|---|---|
| `-address` | 浏览器入口监听地址，默认 `:8080` |
| `-upstream` | 控制面 API 基地址，默认 `http://127.0.0.1:8081` |
| `-secure-cookies` | TLS 之后设置会话 Cookie 的 `Secure` |
| `-frame-origin` | 允许窗口内嵌的应用 origin（KOSD-F-18），默认只允许同源 |

`KOS_DASHBOARD_SESSION_KEY` 是会话 Cookie 的封装密钥（32 字节，base64）。未设置时进程生成随机密钥，
重启即结束全部会话。

## API 面

| 路由 | 语义 |
|---|---|
| `POST /api/v1/auth/login` | 登录代理；令牌留在 BFF 的密封 Cookie 里，响应不回传令牌（KOSD-C-63） |
| `POST /api/v1/auth/logout` | 退出代理并清除会话 Cookie |
| `GET /api/v1/desktop` | 桌面页投影（DASH-C-52） |
| `GET /api/v1/stream` | 事件 SSE；读取失败时保留上次观测并标陈旧（KOSD-C-13、KOSD-C-64） |
| `/api/v1/*` | 控制面 API 代理；响应与错误包络原样透传（KOSD-C-12） |
| `GET /healthz` | 自身健康，带观测时间 |
| `/` | 浏览器入口与静态资产 |

写请求需要 `X-CSRF-Token`，其值来自登录响应下发的 `kos_bff_csrf` Cookie。

## 测试

```sh
make lint        # gofmt + go vet + 外壳模块语法检查
make js-test     # 外壳用例（node --test）
make test        # 单元与验收用例 + 外壳用例，需要 envtest（自动下载并校验 sha512）
make acceptance  # 只跑 tests/acceptance，一条命令
```

外壳用例放 `tests/shell/`，以控制面回答的注册表与桌面页投影为输入，断言外壳渲染出的文档。

验收用例放实现仓 `tests/acceptance/<TC 编号>/`，与设计仓 `kos-dashboard/03-acceptance.md` 同名：

| 目录 | 判据 |
|---|---|
| `tests/acceptance/DASH-TC-1/` | 桌面页四类展示项逐项可读且带来源；通知中心与对象事件一致；卸载应用后其条目不再列出，通知历史不变 |
| `tests/acceptance/DASH-TC-59/` | 删除两个条目中的一个后应用阶段、副本数与下游不变，剩余条目仍指向该应用 |
| `tests/acceptance/DASH-TC-60/` | 下游工作负载与入口改名后条目仍指向同一应用，可见性与排序不变，解析地址随入口更新 |

## 依赖

- kosd：控制面 API、会话与偏好存储、扩展声明与控制面页面投影（`github.com/tsic404/kosd`）。
- 系统应用：各自贡献页面或窗口应用；桌面只托管，不调用其内部接口。

## 非目标

- 不直接访问运行内核；BFF 不复制业务规则，不持有集群凭据。
- 不把控制台会话 Cookie 或集群凭据交给应用。
- 不持久化窗口打开集合、z-order、焦点与最小化状态。
- 不实现文件浏览、应用安装或设置编辑等系统应用功能。
