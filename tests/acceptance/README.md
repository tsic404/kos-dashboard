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
| `DASH-TC-140/` | DASH-AC-67 / DASH-TC-140 安装、打开与卸载窗口应用 | 本仓 |
| `DASH-TC-141/` | DASH-AC-67 / DASH-TC-141 未声明、未安装与缺字段 | 本仓 |
| `DASH-TC-162/` | DASH-AC-71 / DASH-TC-162 页面渲染、动作与停用 | 本仓 |
| `DASH-TC-163/` | DASH-AC-71 / DASH-TC-163 页面不得成为 iframe 入口 | 本仓 |

`DASH-TC-162` 与 `DASH-TC-163` 的 Go 部分覆盖后端半边：页面声明与注册表、服务端投影逐项的当前值/来源/修改入口/只读原因、动作的 202 已接受与对象引用、幂等重放、进度与终态取自对象状态与条件、随组件启停的入口消失与直接调用被拒、无权限账号的同型拒绝，以及页面与 `App` 的导航与 iframe 边界。浏览器侧渲染由前端半边补齐。

`DASH-TC-59` 与 `DASH-TC-60` 随桌面条目类从 `tsic404/kosd` 迁入；kosd 仓不再保留这两个目录。

同一目录下的 `shell.test.js` 是用例的浏览器侧部分：它加载外壳模块，用假浏览器与假控制面回答驱动 dock、launcher、iframe 挂载与 `ready` 握手。`go test ./tests/...` 跑 Go 部分，`make js-test` 跑浏览器部分。

## 执行

```sh
make acceptance   # 一条命令跑本目录全部用例，需要 KUBEBUILDER_ASSETS
```

`make acceptance` 会先下载并校验 envtest 资产。只想跑一个用例时：

```sh
KUBEBUILDER_ASSETS=$PWD/bin/envtest go test ./tests/acceptance/DASH-TC-1/ -count=1 -v
```
