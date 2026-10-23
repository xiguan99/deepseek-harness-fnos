# DeepSeek Harness for fnOS

为飞牛 OS（fnOS）打包的 DeepSeek Harness 管理与部署应用：把 DSH 运行时、WebUI 与管理面板装成一个原生应用，从飞牛桌面点开即用。

---

## 主要功能

- **服务控制**：启动、停止、重启、检查更新与重新部署，内置进程组清理
- **插件管理**：支持 npm 包 / Git 仓库安装，构建脚本自动放行，一键启停与卸载
- **工作区查看**：同步工作区列表、关联会话数与更新时间，可跳转文件管理定位目录
- **安全与网关**：支持飞牛统一网关、HTTP 反代与自定义地址，可选访问密码鉴权（见 [反代适配文档](./REVERSE_PROXY_ADAPTATION.md)）
- **运行日志**：WebSocket 实时推送，语法高亮、自动滚动、日志轮转、清空与下载
- **应用设置**：服务端口、反代端口、打开方式、访问密码、网络代理，带端口占用检测与热重载

两个桌面入口：

| 入口 | 打开 | 说明 |
| --- | --- | --- |
| Harness Admin | `/app/deepseek-harness/` | 管理与运维面板 |
| Harness WebUI | `/app/deepseek-harness/fngateway/` | DSH 本体 WebUI，支持飞牛统一网关、FnConnect、外部反代 |

---

## 系统要求

- 飞牛 OS（fnOS）x86_64
- 依赖应用中心的 `nodejs_v24`（安装时自动带上）
- 磁盘：应用约 180 MB；数据目录（会话、插件、记忆）随使用增长
- 内存：DSH 运行时单进程约 200～400 MB，长会话更高，可在设置里调堆内存上限

---

## 运行时版本策略

本应用只维护「应用层」（启停生命周期、桌面入口、管理面板、更新器、安装回调），**DSH 运行时用官方 npm 包并钉死版本**：

- 当前应用版本 `0.4.1.1`，内置运行时 `@deepseek-ai/dsh` **0.2.1-rc.2**
- 运行时随安装包分发（包内 `deepseek-harness.tar.gz`，含 `package.json` / `package-lock.json` / `node_modules`），首启解包，无需联网
- 面板里的「在线升级运行时」**默认关闭**：DSH 版本跟随应用包，升级应用时一起更新。需要临时尝鲜时可手动开启，开启后运行版本会与包内版本不一致
- 覆盖安装只替换程序，不动数据目录里的会话、插件与记忆

---

## 安装

1. 应用中心 → 手动安装 → 选择 `deepseek.harness-x86.fpk`
2. 首次启动会解出运行时并准备数据目录，通常 1～2 分钟
3. 从桌面图标进入 Harness Admin，按需配置端口、访问方式与密码

---

## 构建与打包

构建全部在仓库外完成，产物落在构建目录，不写系统目录：

```bash
# 一次性准备：Go 1.23.12、Node 24（应用中心 nodejs_v24 自带）
# 一键构建：版本与图标自检 → 运行时 tar（复用或重建）→ 前端 → Go → 打 fpk
./scripts/build.sh

# 可选参数
./scripts/build.sh --runtime         # 强制重建运行时 tar（会 npm install 到构建目录）
./scripts/build.sh --skip-frontend   # 跳过前端构建，复用已有 dist
```

构建脚本会先做硬校验，任一不通过直接中止：

- 版本一致：`VERSION`（应用版本）与 `fnpack/manifest`、`README` 必须一致；`RUNTIME_VERSION` 与运行时 `package.json`、`package-lock.json` 必须一致，且依赖必须**精确钉死**（不接受 `^` / `latest`）
- **实物版本校验**：把运行时 tar 解开，读 `node_modules/@deepseek-ai/dsh/package.json` 里实际安装的版本，必须等于 `RUNTIME_VERSION`（防止"声明一个版本、实际装的是另一个"）
- 图标：`ICON.PNG` / `ICON_256.PNG` 必须存在、256×256、四角透明，且不得与同目录其它应用图标重复
- 端口钉死：默认内部端口 2298、反代端口 2299，桌面入口固定走 `web.sock` 与 `/app/deepseek-harness` 前缀，被改动即中止
- 目录守卫：拒绝在应用数据目录内执行任何构建

升级可用性单独验证（隔离目录、独立端口，不碰已装的应用）：

```bash
./scripts/smoke-upgrade.sh          # 先装旧版，再覆盖到当前包，验数据不丢
./scripts/smoke-upgrade.sh --keep   # 保留工作目录便于排查
```

它会检查：`dsh-data` 里的会话与配置文件不变、配置项（端口、依赖源）沿用升级前的值、`web.sock` 重建、管理面板与 WebUI 网关可用、停止后端口释放、卸载回调不动数据目录。

产物：`<构建目录>/dist/deepseek.harness_v<版本>_x86.fpk`

---

## 隐私

应用只做本地管理：不含设备指纹、使用统计、远程校验或遥测上报。唯一的出网请求是「检查更新」时访问本项目 Releases 页面与 npm 版本接口，可离线使用。

---

## 版本

- 应用：`0.4.1.1`
- 内置运行时：`@deepseek-ai/dsh` `0.2.1-rc.2`

---

## 来源与致谢

- DSH 运行时：DeepSeek，官方 npm 包 `@deepseek-ai/dsh`
- 应用层最初由 [yuexps/deepseek.harness.fnos](https://github.com/yuexps/deepseek.harness.fnos) 实现，本仓库在其 v0.4.1 基础上自维护
- 许可证：随上游，上游仓库未附许可证文件；如需商用请先与上游确认
