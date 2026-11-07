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
| `pkg/dock` | 桌面条目对象类（DASH-C-30）、桌面页声明（DASH-C-52）、dock 偏好域（DASH-C-27）、条目生命周期控制器（KOSD-C-19/20） |
| `pkg/desktop` | 桌面页投影：条目、已安装应用、最近使用与通知中心，逐项带来源（KOSD-C-53） |
| `pkg/apphost` | 窗口应用：`App` 清单装载与校验（KOSD-C-73）、launcher/dock 共用清单、窗口实例与短期应用令牌、`ready` 握手、权限确认与撤销、审计（KOSD-C-75、KOSD-C-14）、资产地址签名 |
| `pkg/bff` | 会话引导与登录代理、控制面 API 代理与错误包络、事件 SSE、CSRF/CSP、应用宿主路由、静态资产 |
| `pkg/bff/assets/shell` | 浏览器外壳：启动、路由、错误边界、页面注册表导航、桌面页渲染、事件流、dock 与 launcher 界面、应用窗口挂载与 `ready` 握手调用 |
| `cmd/kos-dashboard` | BFF 进程入口 |

### 条目控制器的宿主契约

控制器以库形式交付（`dock.NewController`），由运行时宿主注册与投递：

- 宿主按声明的 `scope` 注册 `desktop-entry`（`extension.ControllerSet.Register`），并把控制器 `ReadScope` 声明的内核 kind（`Application`）的调和请求投给它。声明本身只列本扩展的对象类，观察集的声明式表达由框架侧补足。
- 组件激活时先调用 `Sync`：它扫描存储中的条目，删掉所属应用已卸载的条目，然后才对外提供条目类。
- 控制器只写 `desktop.kos.tsic.top` 组的对象；内核对象与其他扩展的对象只读。

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
KOS_DASHBOARD_SESSION_KEY=$(head -c 32 /dev/urandom | base64 | tr -d '\n=') \
  go run ./cmd/kos-dashboard -address :8080 -upstream http://127.0.0.1:8081
```

| 参数 | 语义 |
|---|---|
| `-address` | 浏览器入口监听地址，默认 `:8080` |
| `-upstream` | 控制面 API 基地址，默认 `http://127.0.0.1:8081` |
| `-secure-cookies` | TLS 之后设置会话 Cookie 的 `Secure` |
| `-frame-origin` | 允许窗口内嵌的应用 origin（KOSD-F-18），默认只允许同源 |
| `-console-origin` | 必填。本进程服务控制台所用的 origin；应用入口与该 origin 相同即声明无效，资产也拒绝在此 origin 分发（KOSD-F-18） |
| `-app-assets-root` | 必填。已校验应用包的解包目录，布局为 `<extension>/<version>/<digest>/<asset>`；签名资产地址只从这里分发（KOSD-C-73） |
| `-allow-insecure-app-entries` | 允许 loopback 上的明文 HTTP 应用入口，供本地部署使用（KOSD-C-73） |

`KOS_DASHBOARD_SESSION_KEY` 是会话 Cookie 的封装密钥（32 字节，无填充 base64；资产地址签名与它共用）。未设置时进程生成随机密钥，
重启即结束全部会话。

## API 面

| 路由 | 语义 |
|---|---|
| `POST /api/v1/auth/login` | 登录代理；令牌留在 BFF 的密封 Cookie 里，响应不回传令牌（KOSD-C-63） |
| `POST /api/v1/auth/logout` | 退出代理并清除会话 Cookie |
| `GET /api/v1/desktop` | 桌面页投影（DASH-C-52） |
| `GET /api/v1/stream` | 事件 SSE；读取失败时保留上次观测并标陈旧（KOSD-C-13、KOSD-C-64） |
| `GET /api/v1/apps` | launcher 与 dock 共用的应用清单；只列已安装、清单有效的 `App`，同时给出被拒项与原因（DASH-C-30、KOSD-C-73） |
| `GET /api/v1/apps/{id}` | 单个应用 |
| `POST /api/v1/apps/{id}/windows` | 打开窗口：返回清单默认尺寸、实例模式、沙箱与签名资产地址；`single` 在每个浏览器会话内只一处，`window.hasToken` 标注该实例是否已交付令牌，供刷新后先关旧实例再开新实例（DASH-C-30、DASH-C-74、KOSD-C-73、KOSD-C-75） |
| `GET /api/v1/windows` | 本账号的窗口实例与状态；应用卸载后已打开窗口为 `error`（DASH-C-30） |
| `POST /api/v1/windows/{window}/handshake` | 控制台侧 `ready` 握手：校验 origin、nonce 与消息结构后交付一次 `init`（KOSD-C-75） |
| `DELETE /api/v1/windows/{window}` | 关窗并撤销令牌（KOSD-C-75） |
| `GET /api/v1/apps/{id}/permissions` | 应用声明的能力与账号确认状态 |
| `PUT/DELETE /api/v1/apps/{id}/permissions/{permission}` | 确认或撤销能力；撤销立即失效当前窗口（KOSD-C-75） |
| `POST /api/v1/windows/{window}/capabilities/{permission}` | 使用一项能力并写审计；未确认返回 `consent_required`（KOSD-C-75、KOSD-C-14） |
| `GET /api/v1/deep-links?path=` | 深链解析；未声明或不存在的应用不创建窗口（DASH-C-30） |
| `GET /app-assets/{extension}/{app}/{version}/{digest}/{path}` | 应用 UI 资产：按扩展、应用、版本与包摘要寻址，校验覆盖路径的短期签名与到期；只在应用 origin 上分发，不在控制台 origin 提供（KOSD-C-73、KOSD-F-18） |
| `GET /api/v1/dock` | 账号的 dock 布局与按固定顺序排列的应用；未写入时答默认值（DASH-C-27、DASH-C-30） |
| `GET /api/v1/launcher` | 账号的 launcher 布局与应用列表；未写入时答默认值（DASH-C-27） |
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

外壳用例放 `tests/shell/`，以控制面回答的注册表与桌面页投影为输入，断言外壳渲染出的文档。浏览器侧的验收用例与 Go 用例同目录（`tests/acceptance/<TC 编号>/shell.test.js`），由 `make js-test` 运行。

验收用例放实现仓 `tests/acceptance/<TC 编号>/`，与设计仓 `kos-dashboard/03-acceptance.md` 同名：

| 目录 | 判据 |
|---|---|
| `tests/acceptance/DASH-TC-1/` | 桌面页四类展示项逐项可读且带来源；通知中心与对象事件一致；卸载应用后其条目不再列出，通知历史不变 |
| `tests/acceptance/DASH-TC-59/` | 删除两个条目中的一个后应用阶段、副本数与下游不变，剩余条目仍指向该应用 |
| `tests/acceptance/DASH-TC-60/` | 下游工作负载与入口改名后条目仍指向同一应用，可见性与排序不变，解析地址随入口更新 |
| `tests/acceptance/DASH-TC-140/` | 安装声明窗口应用的扩展后两处出现入口，打开为清单默认尺寸且单例聚焦；卸载后入口消失、已打开窗口进入错误态且不能重开，其他应用不受影响 |
| `tests/acceptance/DASH-TC-141/` | 清单缺必填项的组件不就绪且无入口；只声明页面的扩展无窗口入口而页面仍在导航；未声明或未安装的深链不解析、不建窗 |

## 依赖

- kosd：控制面 API、会话与偏好存储、扩展声明、控制面页面投影与应用注册表（`github.com/tsic404/kosd`）。bff 以调用方会话读取 `/api/v1/apps` 作为已安装应用的来源。
- 系统应用：各自贡献页面或窗口应用；桌面只托管，不调用其内部接口。

## 非目标

- 不直接访问运行内核；BFF 不复制业务规则，不持有集群凭据。
- 不把控制台会话 Cookie 或集群凭据交给应用。
- 不持久化窗口打开集合、z-order、焦点与最小化状态。
- 不实现文件浏览、应用安装或设置编辑等系统应用功能。
