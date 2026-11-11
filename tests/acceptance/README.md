# tests/acceptance

验收用例的可执行形式。目录名等于设计仓 `03-acceptance.md` 的用例编号，一个目录一个用例。

设计依据：`tsic/kos-design@05bf60a`。用例共用 kosd 的验收装配包
`github.com/tsic404/kosd/tests/acceptance/harness`：它启动一次性的 API server（envtest），
安装内核对象类，并把控制面、账号表面与页面表面组合成浏览器要访问的那一个控制面。

## 用例

| 目录 | 设计条目 | 项目 |
|---|---|---|
| `DASH-TC-1/` | DASH-AC-1 / DASH-TC-1 桌面页逐项读取 | 本仓 |
| `DASH-TC-59/` | DASH-AC-29 / DASH-TC-59 删除桌面条目不卸载 | 本仓 |
| `DASH-TC-60/` | DASH-AC-29 / DASH-TC-60 下游改名后条目保持 | 本仓 |
| `DASH-TC-61/` | DASH-AC-30 / DASH-TC-61 关闭全部窗口 | 本仓 |
| `DASH-TC-140/` | DASH-AC-67 / DASH-TC-140 安装、打开与卸载窗口应用 | 本仓 |
| `DASH-TC-141/` | DASH-AC-67 / DASH-TC-141 未声明、未安装与缺字段 | 本仓 |
| `DASH-TC-145/` | DASH-AC-69 / DASH-TC-145 状态转移与关闭否决 | 本仓 |
| `DASH-TC-146/` | DASH-AC-69 / DASH-TC-146 单例、多实例与深链 | 本仓 |
| `DASH-TC-147/` | DASH-AC-69 / DASH-TC-147 错误恢复与工作区 | 本仓 |
| `DASH-TC-148/` | DASH-AC-70 / DASH-TC-148 跨浏览器布局与视口 clamp | 本仓 |
| `DASH-TC-149/` | DASH-AC-70 / DASH-TC-149 布局上限、迁移与版本 | 本仓 |
| `DASH-TC-150/` | DASH-AC-70 / DASH-TC-150 偏好域排除会话状态与数据库降级 | 本仓 |
| `DASH-TC-161/` | DASH-AC-70 / DASH-TC-161 对象、数据库与偏好均无会话状态 | 本仓 |
| `DASH-TC-162/` | DASH-AC-71 / DASH-TC-162 页面渲染、动作与停用 | 本仓 |
| `DASH-TC-163/` | DASH-AC-71 / DASH-TC-163 页面不得成为 iframe 入口 | 本仓 |
| `KOSD-TC-175/` | KOSD-AC-71 / KOSD-TC-175 postMessage 的实例校验 | 本仓 |

`DASH-TC-148`、`DASH-TC-149`、`DASH-TC-150` 与 `DASH-TC-161` 的 Go 部分覆盖后端半边：布局偏好域经控制面偏好 API 读写、会话身份取自控制面、BFF 只代理不复制业务规则、50 条与 64 KiB 上限、旧版本迁移与高版本拒绝、视口 clamp 与显示器回退不回写、数据库不可用时偏好不可读写而集群调和不受影响，以及打开集合/z-order/焦点/最小化不进入对象与偏好。浏览器部分覆盖前端半边：外壳按自己的视口与显示器集合读取适配后的布局、把窗口会话的 `layout` 投影写回偏好（固定项经 `pinned` 会话路由）、按控制面信封呈现超限、版本冲突与数据库不可用的拒绝原因，并断言读取到的 clamp/回退值不回写。

`DASH-TC-162` 与 `DASH-TC-163` 的 Go 部分覆盖后端半边：页面声明与注册表、服务端投影逐项的当前值/来源/修改入口/只读原因、动作的 202 已接受与对象引用、幂等重放、进度与终态取自对象状态与条件、随组件启停的入口消失与直接调用被拒、无权限账号的同型拒绝，以及页面与 `App` 的导航与 iframe 边界。浏览器侧渲染由前端半边补齐。

`DASH-TC-59`、`DASH-TC-60` 与 `DASH-TC-61` 随桌面条目类与窗口管理器从 `tsic404/kosd` 迁入；kosd 仓不再保留这些目录。窗口状态机、实例、工作区与会话几何的 Go 用例经 `tests/acceptance/front` 驱动 BFF 的窗口会话路由；用例共用 kosd 的装配包启动一次性 API server（envtest）。

同一目录下的 `shell.test.js` 是用例的浏览器侧部分：它加载外壳模块，用假浏览器与假控制面回答驱动 dock、launcher、iframe 挂载与 `ready` 握手。`go test ./tests/...` 跑 Go 部分，`make js-test` 跑浏览器部分。

`KOSD-TC-175/` 是例外：Go 部分起真实的前端进程（控制面来自 `harness`，应用资产与帧地址由 `front.StartWithAssets` 签发），再用 `node --test` 在同一用例里加载外壳模块、把前端地址与会话注入浏览器侧。两侧驱动同一个前端，断言读的是它自己的窗口会话与实例状态，没有桩应答；判定窗口的时钟由用例通过前端的时间推进端点移动，不靠真实等待。

## 执行

```sh
make acceptance   # 一条命令跑本目录全部用例，需要 KUBEBUILDER_ASSETS
```

`make acceptance` 会先下载并校验 envtest 资产。只想跑一个用例时：

```sh
KUBEBUILDER_ASSETS=$PWD/bin/envtest go test ./tests/acceptance/DASH-TC-1/ -count=1 -v
```
